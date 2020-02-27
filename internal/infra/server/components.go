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
	taskController "github.com/lloydmeta/tasques/internal/api/controllers/task"
	recurringController "github.com/lloydmeta/tasques/internal/api/controllers/task/recurring"
	"github.com/lloydmeta/tasques/internal/domain/leader"
	"github.com/lloydmeta/tasques/internal/domain/task"
	recurring2 "github.com/lloydmeta/tasques/internal/domain/task/recurring"
	recurring3 "github.com/lloydmeta/tasques/internal/infra/cron/task/recurring"
	"github.com/lloydmeta/tasques/internal/infra/elasticsearch/common"
	"github.com/lloydmeta/tasques/internal/infra/elasticsearch/index"
	"github.com/lloydmeta/tasques/internal/infra/server/routing/tasks"
	"github.com/lloydmeta/tasques/internal/infra/server/routing/tasks/recurring"

	"github.com/lloydmeta/tasques/internal/config"
	infraLeader "github.com/lloydmeta/tasques/internal/infra/elasticsearch/leader"
	infraTask "github.com/lloydmeta/tasques/internal/infra/elasticsearch/task"
	infraRecurring "github.com/lloydmeta/tasques/internal/infra/elasticsearch/task/recurring"
	"github.com/lloydmeta/tasques/internal/infra/server/binding/validation"
	"github.com/lloydmeta/tasques/internal/infra/server/routing"
)

type Components struct {
	Config                      *config.App
	esClient                    *elasticsearch.Client
	taskRoutesHandler           tasks.RoutesHandler
	recurringTasksRoutesHandler recurring.RoutesHandler
	recurringRunningLock        leader.Lock
	recurringRunner             leader.InternalRecurringFunctionRunner
	dynamicScheduler            recurring2.Scheduler
	recurringTasksManager       recurring2.Manager
	logFile                     *os.File
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
		tasksController := taskController.New(tasksService, config.Tasks.Defaults)

		tasksRoutesHandler := tasks.RoutesHandler{
			TasksDefaultsSettings: config.Tasks.Defaults,
			Controller:            tasksController,
		}

		recurringTasksService := infraRecurring.NewService(
			esClient,
			config.Recurring.RecurringTasks.ScrollSize,
			config.Recurring.RecurringTasks.ScrollTtl,
		)
		recurringTasksController := recurringController.New(recurringTasksService, config.Tasks.Defaults)

		recurringTasksRoutesHandler := recurring.RoutesHandler{
			Controller: recurringTasksController,
		}

		dynamicScheduler := recurring3.NewScheduler(tasksService)
		recurringTasksManager := recurring2.NewManager(dynamicScheduler, recurringTasksService)

		recurringRunnerLock := buildRecurringTasksLeaderLock(config.Recurring.LeaderLock, esClient)
		recurringRunner := buildRecurringRunner(config.Recurring, recurringRunnerLock, tasksService, &recurringTasksManager)

		return &Components{
			Config:                      config,
			esClient:                    esClient,
			taskRoutesHandler:           tasksRoutesHandler,
			recurringTasksRoutesHandler: recurringTasksRoutesHandler,
			recurringRunningLock:        recurringRunnerLock,
			recurringRunner:             recurringRunner,
			dynamicScheduler:            dynamicScheduler,
			recurringTasksManager:       recurringTasksManager,
		}, nil
	}
}

func (c *Components) Run() {
	validation.SetUpValidators(c.dynamicScheduler)

	ginRouter := gin.New()

	ginRouter.Use(logger.SetLogger(), apmgin.Middleware(ginRouter), gzip.Gzip(gzip.BestSpeed))
	ginRouter.NoRoute(routing.NoRoute)
	ginRouter.NoMethod(routing.NoMethod)

	topLevelRouterGroup := routing.NewTopLevelRoutesGroup(c.Config.Auth, ginRouter)
	c.taskRoutesHandler.RegisterRoutes(topLevelRouterGroup)
	c.recurringTasksRoutesHandler.RegisterRoutes(topLevelRouterGroup)

	// use ginSwagger middleware to serve the API docs
	ginRouter.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	srv := &http.Server{
		Addr:    c.Config.BindAddress,
		Handler: ginRouter,
	}

	c.recurringRunningLock.Start()
	c.recurringRunner.Start()
	c.dynamicScheduler.Start()

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

	c.dynamicScheduler.Stop()
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

func buildRecurringRunner(
	conf config.Recurring,
	leaderLock leader.Lock,
	tasksService task.Service,
	recurringTasksManager *recurring2.Manager,
) leader.InternalRecurringFunctionRunner {
	recurringFunctions := []leader.InternalRecurringFunction{
		// This task is not *strictly* required, because we can always do some form of clever querying like
		// filtering for FAILED + timedout + remaining attempts > 0 when claiming tasks, but IMHO the data
		// the actual state is easier to understand, and leads to more readable and maintainable code
		leader.NewInternalRecurringFunction(
			"timed-out-task-runner",
			conf.TimedOutTasksReaper.RunInterval,
			func(isLeader leader.Checker) error {
				if isLeader.IsLeader() {
					return tasksService.ReapTimedOutTasks(context.Background(), conf.TimedOutTasksReaper.ScrollSize, conf.TimedOutTasksReaper.ScrollTtl)
				} else {
					log.Debug().Msg("Task reaper skipped because we don't have the lock.")
					return nil
				}
			},
		),
		leader.NewInternalRecurringFunction(
			"recurring-tasks-sync",
			conf.RecurringTasks.SyncRunInterval,
			recurringTasksManager.RecurringSyncFunc(),
		),
		leader.NewInternalRecurringFunction(
			"recurring-tasks-sync-enforcer",
			conf.RecurringTasks.EnforceSyncRunInterval,
			recurringTasksManager.RecurringSyncEnforceFunc(),
		),
	}

	return leader.NewInternalRecurringFunctionRunner(recurringFunctions, leaderLock)
}
