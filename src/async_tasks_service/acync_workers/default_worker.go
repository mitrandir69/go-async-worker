package acync_workers

import (
	"context"
	"github.com/mitrandir69/go-async-worker/src/async_tasks_service"
	"github.com/mitrandir69/go-async-worker/src/status"
)

type DefaultAsyncWorker struct {
	ctx        context.Context
	data       []async_tasks_service.IAsyncAction
	typeAction string
	tasksCount int
}

func NewDefaultAsyncWorker(ctx context.Context, typeAction string, tasksCount int) *DefaultAsyncWorker {
	c := &DefaultAsyncWorker{
		ctx:        ctx,
		data:       make([]async_tasks_service.IAsyncAction, 0),
		typeAction: typeAction,
		tasksCount: tasksCount,
	}
	return c
}

func (h *DefaultAsyncWorker) AddAction(actions ...async_tasks_service.IAsyncAction) {
	h.data = append(h.data, actions...)
}

func (h *DefaultAsyncWorker) Start() error {
	atr := &async_tasks_service.AsyncTasksResult{}
	atr.Status = status.Running
	atr.TypeAction = h.typeAction
	go func() {
		aTService := async_tasks_service.NewService(h.tasksCount)
		for _, value := range h.data {
			aTService.AppendAction(value)
		}
		result := aTService.Run()
		if len(result.Errors) != 0 {
			atr.Status = status.DoneWithErrors
		} else {
			atr.Status = status.Done
		}
		atr.TimeEnd = result.TimeEnd
		atr.TimeStart = result.TimeStart
		atr.Errors = result.Errors
		atr.SuccessFinishCount = result.SuccessFinishCount
	}()
	return nil
}

func (h *DefaultAsyncWorker) StartAndWait() async_tasks_service.AsyncTasksResult {
	atr := async_tasks_service.AsyncTasksResult{}
	atr.Status = status.Running
	atr.TypeAction = h.typeAction
	aTService := async_tasks_service.NewService(h.tasksCount)
	if len(h.data) == 0 {
		return async_tasks_service.AsyncTasksResult{Status: "do not have actions"}
	}
	for _, value := range h.data {
		aTService.AppendAction(value)
	}
	result := aTService.Run()
	if len(result.Errors) != 0 {
		atr.Status = status.DoneWithErrors
	} else {
		atr.Status = status.Done
	}
	atr.TimeEnd = result.TimeEnd
	atr.TimeStart = result.TimeStart
	atr.Errors = result.Errors
	atr.SuccessFinishCount = result.SuccessFinishCount
	return atr
}
