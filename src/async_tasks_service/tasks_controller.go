package async_tasks_service

import (
	"sync/atomic"
	"time"
)

type tasksController struct {
	actions    []IAsyncAction
	taskCount  int
	tasks      []*asyncTask
	resultChan chan asyncTaskResult
	resChan    chan AsyncTasksResult
	workChan   chan IAsyncAction
}

func runTasksController(limit int, actions []IAsyncAction, resChan chan AsyncTasksResult) {
	a := &tasksController{resChan: resChan}
	a.initTasks(limit, actions)
	go a.startWork()
}

func (a *tasksController) initTasks(limit int, actions []IAsyncAction) {
	a.actions = append(a.actions, actions...)
	a.resultChan = make(chan asyncTaskResult)
	a.workChan = make(chan IAsyncAction)
	a.taskCount = limit

	for i := 0; i < limit; i++ {
		task := newAsyncTask()
		go task.workWaiter(a.resultChan, a.workChan)
		a.tasks = append(a.tasks, task)
	}

	go a.initTasksController()
}

func (a *tasksController) initTasksController() {
	var ops uint64 = 0
	resp := AsyncTasksResult{}
	resp.TimeStart = time.Now()
	for {
		select {
		case taskResult := <-a.resultChan:
			c := atomic.AddUint64(&ops, 1)
			if taskResult.err != nil {
				resp.Errors = append(resp.Errors, taskResult.err.Error())
			} else {
				atomic.AddUint64(&resp.SuccessFinishCount, 1)
			}
			if c >= uint64(len(a.actions)) {
				a.closeTasks()
				resp.TimeEnd = time.Now()
				a.resChan <- resp
				return
			}
		}
	}
}

func (a *tasksController) startWork() {
	for _, action := range a.actions {
		a.workChan <- action
	}
}

func (a *tasksController) closeTasks() {
	for _, task := range a.tasks {
		task.close()
	}
}
