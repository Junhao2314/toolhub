//go:build !windows

package agentservice

import "context"

func Run(_ func(context.Context) error) (bool, error) {
	return false, nil
}
