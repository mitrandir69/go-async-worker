package async_tasks_service

type IAsyncAction interface {
	RunAsync() error
}
