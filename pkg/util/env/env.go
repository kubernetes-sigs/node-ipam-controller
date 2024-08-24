package env

import (
	"os"
	"strconv"
	"time"
)

func StringVar(val *string, name string) {
	v := os.Getenv(name)
	if v == "" {
		return
	}
	*val = v
}

func BoolVar(val *bool, name string) {
	value := os.Getenv(name)
	if value == "" {
		return
	}
	v, err := strconv.ParseBool(value)
	if err != nil {
		return
	}
	*val = v
}

func DurationVar(val *time.Duration, name string) {
	value := os.Getenv(name)
	if value == "" {
		return
	}
	v, err := time.ParseDuration(value)
	if err != nil {
		return
	}
	*val = v
}
