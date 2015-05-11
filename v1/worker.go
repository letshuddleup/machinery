package machinery

import (
	"encoding/json"
	"errors"
	"log"
	"reflect"

	"github.com/streadway/amqp"
)

// Worker represents a single worker process
type Worker struct {
	app         *App
	ConsumerTag string
}

// InitWorker - Worker constructor
func InitWorker(app *App, consumerTag string) *Worker {
	return &Worker{
		app:         app,
		ConsumerTag: consumerTag,
	}
}

// Launch starts a new worker process
// The worker subscribes to the default queue
// and processes any incoming tasks registered against the app
func (w *Worker) Launch() error {
	cnf := w.app.GetConfig()
	conn := w.app.GetConnection()

	log.Printf("Launching a worker with the following settings:")
	log.Printf("- BrokerURL: %s", cnf.BrokerURL)
	log.Printf("- Exchange: %s", cnf.Exchange)
	log.Printf("- ExchangeType: %s", cnf.ExchangeType)
	log.Printf("- DefaultQueue: %s", cnf.DefaultQueue)
	log.Printf("- BindingKey: %s", cnf.BindingKey)

	openConn, err := conn.Open()
	if err != nil {
		return err
	}

	defer openConn.Close()
	openConn.WaitForMessages(w)

	return nil
}

// processMessage - handles received messages
// First, it unmarshals the message into a TaskSignature
// Then, it looks whether the task is registered against the app
// If it is registered, it calls signarute's Run method and then calls finalize
func (w *Worker) processMessage(d *amqp.Delivery) {
	s := TaskSignature{}
	json.Unmarshal([]byte(d.Body), &s)

	task := w.app.GetRegisteredTask(s.Name)
	if task == nil {
		log.Printf("Task with a name '%s' not registered", s.Name)
		return
	}

	// Everything seems fine, process the task!
	log.Printf("Started processing %s", s.Name)

	reflectedTask := reflect.ValueOf(task)
	relfectedArgs, err := ReflectArgs(s.Args)
	if err != nil {
		w.finalize(&s, reflect.ValueOf(nil), err)
		return
	}

	results := reflectedTask.Call(relfectedArgs)
	if !results[1].IsNil() {
		w.finalize(&s, reflect.ValueOf(nil), errors.New(results[1].String()))
		return
	}

	// Trigger success or error tasks
	w.finalize(&s, results[0], err)
}

// finalize - handles success and error callbacks
func (w *Worker) finalize(s *TaskSignature, result reflect.Value, err error) {
	if err != nil {
		log.Printf("Failed processing %s", s.Name)
		log.Printf("Error = %v", err)

		for _, errorTask := range s.OnError {
			// Pass error as a first argument to error callbacks
			args := append([]TaskArg{TaskArg{
				Type:  reflect.TypeOf(err).String(),
				Value: reflect.ValueOf(err).Interface(),
			}}, errorTask.Args...)
			errorTask.Args = args
			w.app.SendTask(errorTask)
		}
		return
	}

	log.Printf("Finished processing %s", s.Name)
	log.Printf("Result = %v", result.Interface())

	for _, successTask := range s.OnSuccess {
		if s.Immutable == false {
			// Pass results of the task to success callbacks
			args := append([]TaskArg{TaskArg{
				Type:  result.Type().String(),
				Value: result.Interface(),
			}}, successTask.Args...)
			successTask.Args = args
		}
		w.app.SendTask(successTask)
	}
}
