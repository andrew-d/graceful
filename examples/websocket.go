package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"text/template"
	"time"

	"github.com/andrew-d/graceful"
	"github.com/gorilla/websocket"
)

var mainPage = template.Must(template.New("index.html").Parse(`
<!DOCTYPE html>
<html lang="en">
	<head>
		<title>WebSocket Example</title>

		<script src="//ajax.googleapis.com/ajax/libs/jquery/2.0.3/jquery.min.js"></script>
		<script type="text/javascript">
			$(function() {
				var log = $("#log");
				var conn = new WebSocket("ws://{{$}}/ws");
				conn.onclose = function(evt) {
					var el = $("<div><b>Connection closed.</b></div>");
					el.appendTo(log);
				};
				conn.onmessage = function(evt) {
					var el = $("<div/>").text(evt.data);
					el.appendTo(log);
				};

				$("<div/>").text("Ready").appendTo(log);
			});
		</script>
	</head>

	<body>
		<div id="log"></div>
	</body>
</html>
`))

// This structure is responsible for managing multiple websocket connections.
type WebsocketManager struct {
	ch chan struct{}
	wg sync.WaitGroup
}


var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// You always need to read from a websocket connection, even if only to handle
// the pong messages.
func (m *WebsocketManager) readLoop(conn *websocket.Conn) {
	for {
		if _, _, err := conn.NextReader(); err != nil {
			conn.Close()
			break
		}
	}
}

// Write messages to the websocket connection.
func (m *WebsocketManager) handle(conn *websocket.Conn) {
	var i uint

	// Note: defer is LIFO, so the order below matters
	m.wg.Add(1)
	defer m.wg.Done()
	defer conn.Close()

	go m.readLoop(conn)

	for {
		select {
		case <-time.After(2 * time.Second):
			i++

			msg := fmt.Sprintf("This is message %d", i)
			if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
				fmt.Printf("error sending message: %s\n", err)
			}

		case <-m.ch:
			conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}
	}
}

// Signal all websocket connections to close and finish, and then wait for
// the corresponding goroutines to exit.
func (m *WebsocketManager) Shutdown() {
	close(m.ch)
	m.wg.Wait()
}

// Serve websockets, given a websockets manager.  This returns a closure,
// allowing us to capture the manager for later use.
func serveWebsockets(mgr *WebsocketManager) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Printf("error upgrading: %s\n", err)
			return
		}

		mgr.handle(ws)
	}
}

// Serve the index page.  We use a template to fill in the proper hostname.
func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "Not found", 404)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", 405)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	mainPage.Execute(w, r.Host)
}

func main() {
	mgr := &WebsocketManager{
		ch: make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", serveIndex)
	mux.HandleFunc("/ws", serveWebsockets(mgr))

	srv := graceful.NewServer()

	// When we get a signal, we shutdown and wait for all websocket connections
	// first, and then tell the server to exit.
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		fmt.Println("Shutting down websockets...")
		mgr.Shutdown()

		fmt.Println("Shutting down server...")
		srv.Shutdown <- struct{}{}
	}()

	fmt.Println("Starting...")
	srv.Run(":3000", mux)
	fmt.Println("Finished")
}
