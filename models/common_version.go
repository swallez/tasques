// Code generated by go-swagger; DO NOT EDIT.

package models

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	strfmt "github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"
)

// CommonVersion common version
// swagger:model common.Version
type CommonVersion struct {

	// primary term
	PrimaryTerm int64 `json:"primary_term,omitempty"`

	// seq num
	SeqNum int64 `json:"seq_num,omitempty"`
}

// Validate validates this common version
func (m *CommonVersion) Validate(formats strfmt.Registry) error {
	return nil
}

// MarshalBinary interface implementation
func (m *CommonVersion) MarshalBinary() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return swag.WriteJSON(m)
}

// UnmarshalBinary interface implementation
func (m *CommonVersion) UnmarshalBinary(b []byte) error {
	var res CommonVersion
	if err := swag.ReadJSON(b, &res); err != nil {
		return err
	}
	*m = res
	return nil
}
