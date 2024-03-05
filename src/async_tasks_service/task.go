/*
 * Copyright (c) RecFaces 2023.
 * All rights reserved.
 */

package async_tasks_service

type asyncTask struct {
	closeChan chan struct{}
}

func newAsyncTask() *asyncTask {
	return &asyncTask{
		closeChan: make(chan struct{}),
	}
}

type asyncTaskResult struct {
	err error
}

func (a *asyncTask) close() {
	a.closeChan <- struct{}{}
}

func (a *asyncTask) workWaiter(resultChan chan asyncTaskResult, workChan chan IAsyncAction) {
	defer close(a.closeChan)
	for {
		select {
		case action := <-workChan:
			err := action.RunAsync()
			resultChan <- asyncTaskResult{
				err: err,
			}
		case <-a.closeChan:
			return
		}
	}
}
