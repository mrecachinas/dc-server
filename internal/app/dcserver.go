package app

import (
	"context"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/labstack/gommon/log"
	"github.com/mrecachinas/dcserver/internal/app/config"
	"github.com/streadway/amqp"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"os"
	"os/signal"
	"time"

	"net/http"

	"github.com/mrecachinas/dcserver/internal/api"
)

// Run is the main entrypoint into the DCServer app.
// It handles parsing the command-line, setting up
// connections to MongoDB and RabbitMQ, and
// instantiates and runs the echo server.
func Run() {
	cfg := config.NewConfigFromCLI()

	dcapi, err := NewDCAPI(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcapi.DB.Disconnect(ctx)
	defer dcapi.AMQPClient.Close()
	defer dcapi.AMQPChannel.Close()

	e := SetupEchoServer(cfg, dcapi)

	// Run server
	address := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	go func() {
		if err := e.Start(address); err != nil {
			e.Logger.Info("shutting down the server")
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with a timeout of 10 seconds.
	// Use a buffered channel to avoid missing signals as recommended for signal.Notify
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		e.Logger.Fatal(err)
	}
}

// NewDCAPI creates a new api.Api with MongoDB and AMQP connections
// TODO: Maybe move this into api.go? That would require moving
// some of the other setup functions too though...
func NewDCAPI(cfg config.Config) (*api.Api, error) {
	client, mongoConnectionError := SetupMongoConnection(cfg.MongoHost, cfg.MongoPort)
	if mongoConnectionError != nil {
		return nil, mongoConnectionError
	}

	amqpConnection, amqpChannel, amqpError := SetupAMQP(cfg.AMQPHost, cfg.AMQPPort, cfg.AMQPUser, cfg.AMQPPassword)
	if amqpError != nil {
		return nil, amqpError
	}
	dcapi := &api.Api{
		DB:          client,
		AMQPClient:  amqpConnection,
		AMQPChannel: amqpChannel,
		Cfg:         cfg,
	}
	return dcapi, nil
}

// SetupMongoConnection simply (attempts to) connects to MongoDB and returns
// a pointer to a mongo.Client.
func SetupMongoConnection(mongohost string, mongoport int) (*mongo.Client, error) {
	mongouri := fmt.Sprintf("mongodb://%s:%d", mongohost, mongoport)
	client, err := mongo.NewClient(options.Client().ApplyURI(mongouri))
	if err != nil {
		return nil, err
	}
	ctx, _ := context.WithTimeout(context.Background(), 10*time.Second)
	err = client.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// SetupAMQP simply (attempts to) connects to RabbitMQ (or some other AMQP broker)
// and returns pointers to amqp.Connection and amqp.Channel.
func SetupAMQP(amqphost string, amqpport int, amqpuser string, amqppassword string) (*amqp.Connection, *amqp.Channel, error) {
	uri := fmt.Sprintf("amqp://%s:%s@%s:%d/", amqpuser, amqppassword, amqphost, amqpport)
	conn, err := amqp.Dial(uri)
	if err != nil {
		return nil, nil, err
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, nil, err
	}

	return conn, ch, nil
}

// SetupEchoServer sets up the actual webserver and connects
// the routes to the route handler functions.
func SetupEchoServer(cfg config.Config, dcapi *api.Api) *echo.Echo {
	// Setup server
	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// If we're in debug mode, allow CORS
	if cfg.Debug {
		e.Use(middleware.CORS())
		e.Logger.SetLevel(log.DEBUG)
	} else {
		e.Logger.SetLevel(log.INFO)
	}

	// Setup routes
	e.GET("/", func(c echo.Context) error {
		return c.String(http.StatusOK, "Hello, World!")
	})
	e.GET("/api/status", dcapi.GetAllStatus)
	e.GET("/api/tasks", dcapi.GetTasks)
	e.POST("/api/tasks/create", dcapi.CreateTask)
	e.POST("/api/tasks/:id/stop", dcapi.StopTask)
	e.GET("/ws", dcapi.HelloWebsocket)
	e.File("/", "public/index.html")
	return e
}