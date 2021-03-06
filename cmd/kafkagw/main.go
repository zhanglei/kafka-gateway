package main

import (
	"context"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	sarama "github.com/Shopify/sarama"
	"github.com/go-kit/kit/log"
	"github.com/gorilla/handlers"
	"github.com/urfave/cli"
	"google.golang.org/grpc"

	"github.com/moul/kafka-gateway/gen/endpoints"
	"github.com/moul/kafka-gateway/gen/pb"
	"github.com/moul/kafka-gateway/gen/transports/grpc"
	"github.com/moul/kafka-gateway/gen/transports/http"
	"github.com/moul/kafka-gateway/service"
)

func main() {
	app := cli.NewApp()
	app.Name = "kafkagw"
	app.Usage = "Kafka Gateway (gRPC + http)"
	app.Flags = []cli.Flag{
		cli.StringSliceFlag{
			Name:   "brokers, b",
			Usage:  "List of Kafka Brokers",
			EnvVar: "KAFKA_BROKERS",
			Value:  &cli.StringSlice{"127.0.0.1:9092"},
		},
	}
	app.Action = action
	app.Run(os.Args)
}

func action(c *cli.Context) error {
	mux := http.NewServeMux()
	ctx := context.Background()
	errc := make(chan error)
	s := grpc.NewServer()
	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(os.Stdout)
		logger = log.NewContext(logger).With("ts", log.DefaultTimestampUTC)
		logger = log.NewContext(logger).With("caller", log.DefaultCaller)
	}

	var kafkaSyncProducer sarama.SyncProducer
	{
		config := sarama.NewConfig()
		config.Producer.RequiredAcks = sarama.WaitForAll
		config.Producer.Retry.Max = 5
		config.Producer.Return.Successes = true
		var err error
		kafkaSyncProducer, err = sarama.NewSyncProducer(c.StringSlice("brokers"), config)
		if err != nil {
			stdlog.Printf("Failed to initiate sarama.SyncProducer: %v", err)
			os.Exit(-1)
		}
	}

	{
		svc := kafkasvc.New(kafkaSyncProducer, c.StringSlice("brokers"))
		endpoints := kafka_endpoints.MakeEndpoints(svc)
		srv := kafka_grpctransport.MakeGRPCServer(ctx, endpoints)
		kafkapb.RegisterKafkaServiceServer(s, srv)
		kafka_httptransport.RegisterHandlers(ctx, svc, mux, endpoints)
	}

	// start servers
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	go func() {
		logger := log.NewContext(logger).With("transport", "HTTP")
		logger.Log("addr", ":8000")
		errc <- http.ListenAndServe(":8000", handlers.LoggingHandler(os.Stderr, mux))
	}()

	go func() {
		logger := log.NewContext(logger).With("transport", "gRPC")
		ln, err := net.Listen("tcp", ":9000")
		if err != nil {
			errc <- err
			return
		}
		logger.Log("addr", ":9000")
		errc <- s.Serve(ln)
	}()

	logger.Log("exit", <-errc)
	return nil
}
