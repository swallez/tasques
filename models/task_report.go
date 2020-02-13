// Code generated by go-swagger; DO NOT EDIT.

package models

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"github.com/go-openapi/errors"
	strfmt "github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"
	"github.com/go-openapi/validate"
)

// TaskReport task report
// swagger:model task.Report
type TaskReport struct {

	// at
	// Required: true
	// Format: date-time
	At *strfmt.DateTime `json:"at"`

	// data
	Data interface{} `json:"data,omitempty"`
}

// Validate validates this task report
func (m *TaskReport) Validate(formats strfmt.Registry) error {
	var res []error

	if err := m.validateAt(formats); err != nil {
		res = append(res, err)
	}

	if len(res) > 0 {
		return errors.CompositeValidationError(res...)
	}
	return nil
}

func (m *TaskReport) validateAt(formats strfmt.Registry) error {

	if err := validate.Required("at", "body", m.At); err != nil {
		return err
	}

	if err := validate.FormatOf("at", "body", "date-time", m.At.String(), formats); err != nil {
		return err
	}

	return nil
}

// MarshalBinary interface implementation
func (m *TaskReport) MarshalBinary() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return swag.WriteJSON(m)
}

// UnmarshalBinary interface implementation
func (m *TaskReport) UnmarshalBinary(b []byte) error {
	var res TaskReport
	if err := swag.ReadJSON(b, &res); err != nil {
		return err
	}
	*m = res
	return nil
}
