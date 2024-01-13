package concurrency

type ValueTask[T any] struct {
	ch    chan struct{}
	value *T
}

func NewValueTask[T any](f func() *T) *ValueTask[T] {
	task := &ValueTask[T]{ch: make(chan struct{}, 1), value: nil}
	go func() {
		defer close(task.ch)
		task.value = f()
	}()
	return task
}

func (t *ValueTask[T]) Ready() <-chan struct{} {
	return t.ch
}

func (t *ValueTask[T]) WaitForValue() *T {
	<-t.ch
	return t.value
}
