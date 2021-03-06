package server

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"go.elastic.co/apm/module/apmgin"

	"github.com/gin-contrib/gzip"
	"github.com/swaggo/gin-swagger"
	"github.com/swaggo/gin-swagger/swaggerFiles"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/gin-contrib/logger"
	"github.com/gin-gonic/gin"

	// This is generated by swaggo
	_ "github.com/lloydmeta/tasques/docs"
	"github.com/lloydmeta/tasques/internal/domain/leader"
	"github.com/lloydmeta/tasques/internal/domain/task"
	"github.com/lloydmeta/tasques/internal/infra/elasticsearch/common"
	"github.com/lloydmeta/tasques/internal/infra/elasticsearch/index"

	"github.com/lloydmeta/tasques/internal/api/controllers"
	"github.com/lloydmeta/tasques/internal/config"
	infraLeader "github.com/lloydmeta/tasques/internal/infra/elasticsearch/leader"
	infraTask "github.com/lloydmeta/tasques/internal/infra/elasticsearch/task"
	"github.com/lloydmeta/tasques/internal/infra/server/binding/validation"
	"github.com/lloydmeta/tasques/internal/infra/server/routing"
)

type Components struct {
	Config               *config.App
	esClient             *elasticsearch.Client
	taskRoutesHandler    routing.TasksRoutesHandler
	recurringRunningLock leader.Lock
	recurringRunner      leader.RecurringTaskRunner
	logFile              *os.File
}

func NewComponents(config *config.App) (*Components, error) {
	esClient, err := common.NewClient(config.Elasticsearch)

	if err != nil {
		return nil, err
	} else {

		indexTemplateChecker := index.DefaultTemplateSetup(esClient)
		if err = indexTemplateChecker.Check(context.Background()); err != nil {
			return nil, err
		}

		tasksService := infraTask.NewService(esClient, config.Tasks.Defaults)
		tasksController := controllers.NewTasksController(tasksService, config.Tasks.Defaults)

		handler := routing.TasksRoutesHandler{
			TasksDefaultsSettings: config.Tasks.Defaults,
			AuthSettings:          config.Auth,
			Controller:            tasksController,
		}

		recurringRunnerLock := buildRecurringTasksLeaderLock(config.Recurring.LeaderLock, esClient)
		recurringRunner := buildRecurringRunner(config.Recurring, tasksService, recurringRunnerLock)

		return &Components{
			Config:               config,
			esClient:             esClient,
			taskRoutesHandler:    handler,
			recurringRunningLock: recurringRunnerLock,
			recurringRunner:      recurringRunner,
		}, nil
	}
}

func (c *Components) Run() {
	validation.SetUpValidators()

	ginRouter := gin.New()

	ginRouter.Use(logger.SetLogger(), apmgin.Middleware(ginRouter), gzip.Gzip(gzip.BestSpeed))
	ginRouter.NoRoute(routing.NoRoute)
	ginRouter.NoMethod(routing.NoMethod)

	c.taskRoutesHandler.RegisterRoutes(ginRouter)

	// use ginSwagger middleware to serve the API docs
	ginRouter.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	srv := &http.Server{
		Addr:    c.Config.BindAddress,
		Handler: ginRouter,
	}

	c.recurringRunningLock.Start()
	c.recurringRunner.Start()

	go func() {
		// Serve connections
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("Failed to open listener for server.")
		}
	}()

	// Wait for interrupt signals to gracefully shut the server down with a configurable timeout
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Info().Msg("Server shutdown initialised ...")

	c.recurringRunner.Stop()
	c.recurringRunningLock.Stop()

	// Handle shutdown procedures here.
	ctx, cancel := context.WithTimeout(context.Background(), c.Config.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal().Err(err).Msg("Server killed")
	}

	log.Info().Msg("Server gracefully exiting")

}

func buildRecurringTasksLeaderLock(conf config.LeaderLock, esClient *elasticsearch.Client) leader.Lock {
	return infraLeader.NewLeaderLock(
		"recurring-tasks-leader",
		esClient,
		conf.CheckInterval,
		conf.ReportLagTolerance,
	)
}

func buildRecurringRunner(conf config.Recurring, tasksService task.Service, leaderLock leader.Lock) leader.RecurringTaskRunner {
	recurringTasks := []leader.RecurringTask{
		// This task is not *strictly* required, because we can always do some form of clever querying like
		// filtering for FAILED + timedout + remaining attempts > 0 when claiming tasks, but IMHO the data
		// the actual state is easier to understand, and leads to more readable and maintainable code
		leader.NewRecurringTask(
			"timed-out-task-runner",
			conf.TimedOutTasksReaper.RunInterval,
			func() error {
				return tasksService.ReapTimedOutTasks(context.Background(), conf.TimedOutTasksReaper.ScrollSize, conf.TimedOutTasksReaper.ScrollTtl)
			},
		),
	}

	return leader.NewRecurringTaskRunner(recurringTasks, leaderLock)
}
