package policy

import (
	"fmt"
	"strings"
)

type Draft struct {
	Timezone                string                       `json:"timezone"`
	Environments            map[string]EnvironmentPolicy `json:"environments"`
	Exclusions              []Exclusion                  `json:"exclusions"`
	MinimumMetricCoverage   float64                      `json:"minimumMetricCoverage"`
	RequireHumanApproval    bool                         `json:"requireHumanApproval"`
	NeverModifyStatefulSets bool                         `json:"neverModifyStatefulSets"`
}

type EnvironmentPolicy struct {
	AllowedActions  []string `json:"allowedActions"`
	MinimumReplicas int32    `json:"minimumReplicas"`
	SleepAfter      string   `json:"sleepAfter,omitempty"`
	WakeAt          string   `json:"wakeAt,omitempty"`
}

type Exclusion struct {
	Labels map[string]string `json:"labels"`
	Reason string            `json:"reason"`
}

func (draft Draft) Validate() error {
	if strings.TrimSpace(draft.Timezone) == "" {
		return fmt.Errorf("timezone is required")
	}
	if len(draft.Environments) == 0 {
		return fmt.Errorf("at least one environment policy is required")
	}
	if draft.MinimumMetricCoverage < 0.8 || draft.MinimumMetricCoverage > 1 {
		return fmt.Errorf("minimumMetricCoverage must be between 0.8 and 1")
	}
	if !draft.RequireHumanApproval {
		return fmt.Errorf("requireHumanApproval must be true")
	}
	if !draft.NeverModifyStatefulSets {
		return fmt.Errorf("neverModifyStatefulSets must be true")
	}
	allowedActions := map[string]bool{"scale-replicas": true, "scale-to-zero": true, "schedule-sleep": true}
	for environment, rule := range draft.Environments {
		if environment == "production" && len(rule.AllowedActions) != 0 {
			return fmt.Errorf("production allowedActions must be empty in this release")
		}
		if rule.MinimumReplicas < 0 {
			return fmt.Errorf("minimumReplicas cannot be negative")
		}
		for _, action := range rule.AllowedActions {
			if !allowedActions[action] {
				return fmt.Errorf("unsupported action %q", action)
			}
		}
	}
	for _, exclusion := range draft.Exclusions {
		if len(exclusion.Labels) == 0 {
			return fmt.Errorf("each exclusion requires at least one label")
		}
	}
	return nil
}
