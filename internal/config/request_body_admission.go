package config

import (
	"fmt"
	"math"
)

const (
	DefaultRequestBodyAdmissionBudgetBytes  int64 = 128 << 20
	DefaultRequestBodyAdmissionQuantumBytes int64 = 2 << 20
)

// RequestBodyAdmissionConfig bounds the aggregate request-body materialization budget.
type RequestBodyAdmissionConfig struct {
	Enabled      bool  `yaml:"enabled" json:"enabled"`
	BudgetBytes  int64 `yaml:"budget-bytes" json:"budget-bytes"`
	QuantumBytes int64 `yaml:"quantum-bytes" json:"quantum-bytes"`
}

func DefaultRequestBodyAdmissionConfig() RequestBodyAdmissionConfig {
	return RequestBodyAdmissionConfig{
		Enabled:      true,
		BudgetBytes:  DefaultRequestBodyAdmissionBudgetBytes,
		QuantumBytes: DefaultRequestBodyAdmissionQuantumBytes,
	}
}

// Validate checks values used by weighted request-body admission and rounding.
func (c RequestBodyAdmissionConfig) Validate() error {
	if c.BudgetBytes <= 0 {
		return fmt.Errorf("request-body-admission budget-bytes must be positive")
	}
	if c.QuantumBytes <= 0 {
		return fmt.Errorf("request-body-admission quantum-bytes must be positive")
	}
	if c.BudgetBytes > math.MaxInt64-(c.QuantumBytes-1) {
		return fmt.Errorf("request-body-admission budget-bytes and quantum-bytes overflow")
	}
	return nil
}
