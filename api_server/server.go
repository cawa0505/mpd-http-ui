package main

import (
	"fmt"
	"time"
	"log"
	"encoding/json"
	"github.com/gorilla/mux"
	"github.com/gorilla/handlers"
	"github.com/gorilla/websocket"
	"net/http"
	"strconv"
	// mpd_handler "github.com/randomcoww/go-mpd-es/mpd_handler"
	mpd_handler "local/mpd_handler"
	mpd_event "github.com/randomcoww/go-mpd-es/mpd_event"
	es_handler "github.com/randomcoww/go-mpd-es/es_handler"
)

var (
	esIndex, esDocument = "songs", "song"
	mpdClient *mpd_handler.MpdClient
	esClient *es_handler.EsClient
	mpdEvent *mpd_event.MpdEvent

	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)


// Client is a middleman between the websocket connection and the hub.
type Client struct {
	hub *Hub

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan []byte
}


const (
	// Time allowed to write the file to the client.
	writeWait = 10 * time.Second
)


type response struct {
	Message string
}

type socketMessage struct {
	Name string `json:"mutation"`
	Data interface{} `json:"value"`
}


func NewServer(listenUrl, mpdUrl, esUrl string) {

	// backend stuff
	mpdClient = mpd_handler.NewMpdClient("tcp", mpdUrl)
	esClient = es_handler.NewEsClient(esUrl, esIndex, esDocument, "")
	mpdEvent = mpd_event.NewEventWatcher("tcp", mpdUrl)

	// websocket hub
	hub := newHub()
	go hub.run()
	go hub.eventBroadcaster()

	// mux routes
	allowedHeaders := handlers.AllowedHeaders([]string{"X-Requested-With"})
	allowedOrigins := handlers.AllowedOrigins([]string{"*"})
	allowedMethods := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"})

	r := mux.NewRouter()

	r.HandleFunc("/healthcheck", healthCheck).
		Methods("GET")

	r.HandleFunc("/playlist/items", queryPlaylistItems).
		Queries("start", "{start}").
		Queries("end", "{end}").
		Methods("GET")

	r.HandleFunc("/status", queryStatus).
		Methods("GET")

	r.HandleFunc("/currentsong", currentSong).
		Methods("GET")

	r.HandleFunc("/database/search", search).
		Queries("q", "{query}").
		Methods("GET")

	r.HandleFunc("/playlist/items", movePlaylistItems).
		Queries("start", "{start}").
		Queries("end", "{end}").
		Queries("pos", "{moveto}").
		Methods("PUT")

	r.HandleFunc("/playlist/items", deletePlaylistItems).
		Queries("start", "{start}").
		Queries("end", "{end}").
		Methods("DELETE")

	r.HandleFunc("/playlist", addToPlaylist).
		Queries("path", "{path}").
		Methods("PUT")

	r.HandleFunc("/playlist", clearPlaylist).
		Methods("DELETE")

	// websocket handler
	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	// websocket test
	r.HandleFunc("/hometest", serveHome)

	// wait backend start
	<-mpdClient.Ready
	<-esClient.Ready

	// serve http
	fmt.Printf("API server start on %s\n", listenUrl)
	log.Fatal(http.ListenAndServe(listenUrl, handlers.CORS(allowedHeaders, allowedOrigins, allowedMethods)(r)))
}


func parseNum(input string) (int) {
	v, err := strconv.Atoi(input)
	if err != nil {
		fmt.Printf("Error parsing param %s: %s\n", input, err)
		v = -1
	}

	return v
}


func (h *Hub) eventBroadcaster() {
	for {
		select {
		case e := <-mpdEvent.Event:
			fmt.Printf("Got MPD event: %s\n", e)
			h.broadcast <-[]byte(e)
		}
	}
}


func (c *Client) sendStatusMessage() {
	attrs, err := mpdClient.Conn.Status()
	if err != nil {
		return
	}

	err = c.conn.WriteJSON(&socketMessage{Data: attrs, Name: "status"})
	if err != nil {
		fmt.Printf("Failed to write message %s\n", err)
		return
	}
}

func (c *Client) sendCurrentSongMessage() {
	attrs, err := mpdClient.Conn.CurrentSong()
	if err != nil {
		return
	}

	err = c.conn.WriteJSON(&socketMessage{Data: attrs, Name: "currentsong"})
	if err != nil {
		fmt.Printf("Failed to write message %s\n", err)
		return
	}
}

func (c *Client) sendPlaylistMessage() {
	attrs, err := mpdClient.Conn.PlaylistInfo(-1, -1)
	if err != nil {
		return
	}

	err = c.conn.WriteJSON(&socketMessage{Data: attrs, Name: "playlist"})
	if err != nil {
		fmt.Printf("Failed to write message %s\n", err)
		return
	}
}

func (c *Client) sendSeekMessage() {
	attrs, err := mpdClient.Conn.Status()
	if err != nil {
		return
	}

	switch attrs["state"] {
	case "play":
		message := make([]float64, 2)

		elapsed, err := strconv.ParseFloat(attrs["elapsed"], 32)
		if err != nil {
			return
		}

		duration, err := strconv.ParseFloat(attrs["duration"], 32)
		if err != nil {
			return
		}

		message[0] = elapsed
		message[1] = duration

		c.conn.WriteJSON(&socketMessage{Data: message, Name: "seek"})
	}
}


func (c *Client) readSocketEvents() {
	for {
		v := &socketMessage{}

		err := c.conn.ReadJSON(v)
		if err != nil {
			time.Sleep(1000 * time.Millisecond)
			continue
		}

		switch v.Name {
		case "seek":
			t := int64(v.Data.(float64) * 1000000000)
			mpdClient.Conn.SeekCur(time.Duration(t), false)
		}
	}
}


func (c *Client) writeSocketEvents() {
	for {
		select {
		case message, ok := <-c.send:
			if ok {
				switch string(message) {
				case "player":
					c.sendStatusMessage()
					c.sendCurrentSongMessage()

				case "mixer":
					c.sendStatusMessage()

				case "options":
					c.sendStatusMessage()

				case "outputs":
					c.sendStatusMessage()

				case "playlist":
					c.sendPlaylistMessage()
				}
			}

		case <- time.After(1000 * time.Millisecond):
			c.sendSeekMessage()
		}
	}
}


//
// web socket feeder
// based on example https://github.com/gorilla/websocket/blob/master/examples/filewatch/main.go
//
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	// fmt.Printf("Serving WS\n")

	upgrader.CheckOrigin = func(r *http.Request) bool { return true }

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		if _, ok := err.(websocket.HandshakeError); !ok {
			log.Printf("%s\n", err)
		}

		fmt.Printf("Serving WS err %s\n", err)
		return
	}

	client := &Client{
		hub: hub,
		conn: ws,
		send: make(chan []byte, 256),
	}

	client.hub.register <- client
	go client.writeSocketEvents()
	go client.readSocketEvents()
}


//
// handle funcs
//
func healthCheck(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Healthcheck\n")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response{"ok"})
}

func queryPlaylistItems(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Query playlist items %s\n", params)

	attrs, err := mpdClient.Conn.PlaylistInfo(
		parseNum(params["start"]),
		parseNum(params["end"]))
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(attrs)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func queryStatus(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Query status\n")

	attrs, err := mpdClient.Conn.Status()
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(attrs)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func currentSong(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Query current song\n")

	attrs, err := mpdClient.Conn.CurrentSong()
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(attrs)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func movePlaylistItems(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Move playlist items %s\n", params)

	err := mpdClient.Conn.Move(
		parseNum(params["start"]),
		parseNum(params["end"]),
		parseNum(params["moveto"]))
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func addToPlaylist(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Add to playlist %s\n", params)

	err := mpdClient.Conn.Add(
		params["path"])
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func deletePlaylistItems(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Delete playlist items %s\n", params)

	err := mpdClient.Conn.Delete(
		parseNum(params["start"]),
		parseNum(params["end"]))
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func clearPlaylist(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Clear playlist items\n")

	err := mpdClient.Conn.Clear()
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func search(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Search database %s\n", params)

	search, err := esClient.Search(params["query"], 100)
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusOK)

		var	result []*json.RawMessage
		for _, hits := range search.Hits.Hits {
			result = append(result, hits.Source)
		}

		json.NewEncoder(w).Encode(result)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}
