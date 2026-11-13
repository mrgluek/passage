package main

func listenMenu(done <-chan struct{}, clicks <-chan struct{}, f func()) {
	for {
		select {
		case <-done:
			return
		case _, ok := <-clicks:
			if !ok {
				return
			}
			select {
			case <-done:
				return
			default:
				f()
			}
		}
	}
}
