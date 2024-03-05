/*
 * Copyright (c) RecFaces 2023.
 * All rights reserved.
 */

package async_tasks_service

type IAsyncAction interface {
	RunAsync() error
}
