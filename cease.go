package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/streadway/amqp"
)

type RadiodanCommand struct {
	Action        string
	CorrelationId string
}

var dryRun bool

func main() {
	host, port := parseArgs()
	listenForCommand(host, port)
}

func parseArgs() (host string, port int) {
	flag.StringVar(&host, "host", "localhost", "Hostname for RabbitMQ")
	flag.IntVar(&port, "port", 5672, "Port for RabbitMQ")
	flag.BoolVar(&dryRun, "dry-run", false, "Dry Run (do not execute command)")

	flag.Parse()

	return
}

func listenForCommand(host string, port int) {
	var conn *amqp.Connection

	amqpUri := fmt.Sprintf("amqp://%s:%d", host, port)
	exchangeName := "radiodan"
	routingKey := "command.device.shutdown"
	connected := false

	for connected != true {
		tryConn, err := amqp.Dial(amqpUri)
		if err != nil {
			log.Printf("[!] Cannot connect", err)
			log.Printf("[*] Retry in 3 seconds")
			time.Sleep(3 * time.Second)
		} else {
			log.Printf("[*] Connected to %s", amqpUri)

			connected = true
			conn = tryConn
			defer conn.Close()
		}
	}

	consumeChannel, err := conn.Channel()
	failOnError(err, "[!] Failed to open a channel")
	defer consumeChannel.Close()

	replyChannel, err := conn.Channel()
	failOnError(err, "[!] Could not create reply channel")
	defer replyChannel.Close()

	err = consumeChannel.ExchangeDeclare(
		exchangeName, // name
		"topic",      // type
		true,         // durable
		false,        // auto-deleted
		false,        // internal
		false,        // no-wait
		nil,          // arguments
	)
	failOnError(err, "[!] Failed to declare an exchange")

	queue, err := consumeChannel.QueueDeclare(
		"",    // name
		false, // durable
		false, // delete when usused
		true,  // exclusive
		false, // no-wait
		nil,   // arguments
	)
	failOnError(err, "[!] Failed to declare a queue")

	err = consumeChannel.QueueBind(
		queue.Name, // queue name
		routingKey, // routing key
		"radiodan", // exchange
		false,
		nil,
	)
	failOnError(err, "[!] Failed to bind a queue")

	msgs, err := consumeChannel.Consume(
		queue.Name, // queue
		"",         // consumer
		true,       // auto ack
		false,      // exclusive
		false,      // no local
		false,      // no wait
		nil,        // args
	)
	failOnError(err, "[!] Failed to register a consumer")

	log.Println("[*] Consuming", queue.Name)

	forever := make(chan bool)

	go func() {
		for m := range msgs {
			cmd, err := processMessage(m)

			if err == nil {
				replyToMessage(replyChannel, m, cmd, false)
				execCmd(cmd)
			} else {
				replyToMessage(replyChannel, m, cmd, true)
			}
		}
	}()

	log.Printf("[*] Waiting for commands")
	<-forever
}

func processMessage(msg amqp.Delivery) (RadiodanCommand, error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("[!] Msg processing failed:", r)
		}
	}()

	cmd := RadiodanCommand{}

	err := json.Unmarshal(msg.Body, &cmd)
	failOnError(err, "[!] Malformed Radiodan Command")

	log.Printf("[x] Received action: %s", cmd.Action)

	return cmd, err
}

func replyToMessage(replyChannel *amqp.Channel, msg amqp.Delivery,
	cmd RadiodanCommand, parseError bool) {
	response := fmt.Sprintf(
		"{\"error\": %t, \"correlationId\": \"%s\"}",
		parseError, cmd.CorrelationId)

	reply := amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		ContentType:  "text/plain",
		Body:         []byte(response),
	}

	err := replyChannel.Publish(
		"",          // exchange
		msg.ReplyTo, // key
		false,       // mandatory
		false,       // thing
		reply,       // immediate
	)

	failOnError(err, "[!] Could not reply to message")
	log.Println("[*] Replying to message", response)
}

func execCmd(cmd RadiodanCommand) {
	var shutdownFlag, path string
	var args []string

	switch cmd.Action {
	case "restart":
		shutdownFlag = "-r"
	case "shutdown":
		shutdownFlag = "-h"
	default:
		panic("Action " + cmd.Action + " is neither restart nor shutdown")
	}

	if dryRun {
		path = "/bin/echo"
		args = []string{path, "shutdown", shutdownFlag, "now"}
	} else {
		path = "/sbin/shutdown"
		args = []string{path, shutdownFlag, "now"}
	}

	shutdown := exec.Cmd{
		Path: path,
		Args: args,
	}

	output, err := shutdown.CombinedOutput()
	outputStr := strings.TrimRight(string(output), "\n")

	failOnError(err, "Could not exec shutdown")
	log.Println("[x] exec:", outputStr)
}

func failOnError(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %s", msg, err)
		panic(fmt.Sprintf("%s: %s", msg, err))
	}
}
