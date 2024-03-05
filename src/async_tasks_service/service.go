/*
 * Copyright (c) RecFaces 2023.
 * All rights reserved.
 */

package async_tasks_service

import (
	"fmt"
	"strings"
	"time"
)

type Service struct {
	taskLimit    int
	asyncActions []IAsyncAction
}

func NewService(taskLimit int) *Service {
	return &Service{
		taskLimit:    taskLimit,
		asyncActions: make([]IAsyncAction, 0),
	}
}

func (s *Service) AppendAction(action IAsyncAction) {
	s.asyncActions = append(s.asyncActions, action)
}

func (s *Service) Run() AsyncTasksResult {
	resChan := make(chan AsyncTasksResult)
	runTasksController(s.taskLimit, s.asyncActions, resChan)
	return <-resChan
}

type AsyncTasksResult struct {
	Status             string    `json:"status"`
	TypeAction         string    `json:"typeAction"`
	SuccessFinishCount uint64    `json:"successFinishCount"`
	Errors             []string  `json:"errors"`
	TimeStart          time.Time `json:"timeStart"`
	TimeEnd            time.Time `json:"timeEnd"`
}

// to String
func (c *AsyncTasksResult) Error() error {
	if len(c.Errors) == 0 {
		return nil
	}
	return fmt.Errorf(strings.Join(c.Errors, " | "))
}
