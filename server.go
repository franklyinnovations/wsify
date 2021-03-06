package main

import (
	"log"
	"net/http"
	"strings"
)

import (
	"github.com/alash3al/go-pubsub"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo"
	"github.com/labstack/echo/middleware"
	"github.com/rs/xid"
)

var (
	// WSUpgrader is Default websocket upgrader
	WSUpgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			for _, origin := range strings.Split(*FlagAllowedOrigin, ",") {
				origin = strings.TrimSpace(origin)
				if origin == "*" || origin == r.Host {
					return true
				}
			}
			return false
		},
		EnableCompression: true,
	}

	//Broker default
	Broker = pubsub.NewBroker()
)

// WSHandler is the websocket request handler
func WSHandler(c echo.Context) error {
	defer (func() {
		if err := recover(); err != nil {
			log.Println(err)
		}
	})()
	key := c.QueryParam("key")
	if key == "" {
		key = "Anonymous#" + xid.New().String()
	}
	allowed := TriggerWebhook(Event{
		Action: "connect",
		Key:    key,
	})
	if !allowed {
		return c.JSON(403, "You aren't allowed to access this resource")
	}
	conn, err := WSUpgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return nil
	}
	defer conn.Close() // nolint: errcheck
	subscriber, err := Broker.Attach()
	if err != nil {
		conn.WriteJSON(map[string]string{ // nolint: errcheck
			"error": "Sorry, couldn't allocate resources for you",
		})
		return nil
	}
	closeCh := make(chan bool)
	closed := false
	debug("New Valid Connection(" + key + ")")
	conn.SetCloseHandler(func(_ int, _ string) error {
		debug("Connection(" + key + ") has been closed (by itself)")
		closeCh <- true
		return nil
	})
	goRoutineAction(conn, closeCh, subscriber, key)
	for !closed {
		select {
		case <-closeCh:
			closed = true
			Broker.Detach(subscriber)
			TriggerWebhook(Event{Action: "disconnect", Key: key})
		case data := <-subscriber.GetMessages():
			msg := (data.GetPayload()).(Message)
			debug("Incomming message to(" + key + ") ...")
			if !msg.IsUserAllowed(key) {
				debug("The client(" + key + ") isn't allowed to see the message")
				continue
			}
			msg.Topic = data.GetTopic()
			msg.Time = data.GetCreatedAt()
			msg.To = nil
			if err := conn.WriteJSON(msg); err != nil {
				debug("A message cannot be published to (" + key + ") because of the following error (" + err.Error() + ")")
				closeCh <- true
			}
		}
	}
	return nil
}

func goRoutineAction(conn *websocket.Conn, closeCh chan bool, subscriber *pubsub.Subscriber, key string) {
	go (func() {
		var action Event
		for {
			if err := conn.ReadJSON(&action); err != nil {
				debug("Cannot read from the connection of(" + key + "), may connection has been closed, closing ...")
				break
			}
			debug("An action (" + action.Action + ") from the client(" + key + ")")
			if action.Action == "subscribe" || action.Action == "unsubscribe" {
				if !TriggerWebhook(Event{Action: action.Action, Key: key, Value: action.Value}) {
					conn.WriteJSON(map[string]string{ // nolint: errcheck
						"error": "You aren't allowed to access the requested resource",
					})
					continue
				}
			}
			if action.Action == "subscribe" {
				Broker.Subscribe(subscriber, action.Value)
			} else if action.Action == "unsubscribe" {
				Broker.Unsubscribe(subscriber, action.Value)
			}
		}
		close(closeCh)
	})()
}

// PublishHandler ...
func PublishHandler(c echo.Context) error {
	var msg Message
	if err := c.Bind(&msg); err != nil {
		return c.JSON(422, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
	}
	Broker.Broadcast(msg, msg.Topic)
	debug("publishing a message ...")
	return c.JSON(200, map[string]interface{}{
		"success": true,
		"data":    msg,
	})
}

// InitWsServer start the websocket server
func InitWsServer(addr string) error {
	e := echo.New()

	e.Debug = true
	e.HideBanner = true

	e.Pre(middleware.RemoveTrailingSlash())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.Use(middleware.GzipWithConfig(middleware.GzipConfig{Level: 9}))

	e.GET("/subscribe", WSHandler)
	e.POST(*FlagPublishEndpoint, PublishHandler)

	return e.Start(addr)
}
