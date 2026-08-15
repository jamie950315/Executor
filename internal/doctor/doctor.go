package doctor

import "context"

type Checker interface {
	Name() string
	Run(context.Context) (string, error)
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Result struct {
	Healthy bool    `json:"healthy"`
	Checks  []Check `json:"checks"`
}

func Run(ctx context.Context, checkers []Checker) Result {
	result := Result{Healthy: len(checkers) > 0, Checks: make([]Check, 0, len(checkers))}
	for _, checker := range checkers {
		detail, err := checker.Run(ctx)
		check := Check{Name: checker.Name(), OK: err == nil, Detail: detail}
		if err != nil {
			check.Error = err.Error()
			result.Healthy = false
		}
		result.Checks = append(result.Checks, check)
	}
	return result
}
