package main

import (
	"sync"

	"github.com/razecrs/ezpi"
)

type Todo struct {
	ID    int    `json:"id"`
	Title string `json:"title" ez:"required,max=100"`
	Done  bool   `json:"done"`
}

type Login struct {
	Username string `json:"username" ez:"required"`
	Password string `json:"password" ez:"required"`
}

var (
	mu    sync.Mutex
	todos = map[int]Todo{}
	next  = 1
)

func main() {
	app := ezpi.New()
	app.Docs("/docs") // interactive docs at /docs

	app.Get("/health", "ok")

	app.Post("/login", func(l Login) (map[string]string, error) {
		if l.Username != "demo" || l.Password != "demo" { // use a real user store!
			return nil, ezpi.Unauthorized("wrong username or password")
		}
		token, err := app.Token(l.Username)
		return map[string]string{"token": token}, err
	}).Doc("Log in and get a token")

	app.Get("/todos/:id", func(id int) (Todo, error) {
		mu.Lock()
		defer mu.Unlock()
		t, ok := todos[id]
		if !ok {
			return Todo{}, ezpi.NotFound("todo not found")
		}
		return t, nil
	}).Doc("Get a todo")

	app.Private.Post("/todos", func(t Todo) Todo {
		mu.Lock()
		defer mu.Unlock()
		t.ID = next
		next++
		todos[t.ID] = t
		return t
	}).Doc("Create a todo")

	app.Private.Get("/me", func(user ezpi.User) string {
		return "hi " + user.ID
	})

	app.Start()
}
