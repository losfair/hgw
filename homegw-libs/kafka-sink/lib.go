// https://github.com/jeffzhangme/zapx/blob/master/kafka_sink.go

package kafka_sink

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/IBM/sarama"
	"go.uber.org/zap"
)

var TotalCompletedMessages atomic.Uint64

type kafkaSink struct {
	msgChan           chan []byte
	issuedMessages    atomic.Uint64
	completedMessages atomic.Uint64
	topic             string
}

func getKafkaSink(brokers []string, topic string, config *sarama.Config) *kafkaSink {
	producerInst, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		panic(err)
	}

	kafkaSinkInst := &kafkaSink{
		msgChan: make(chan []byte, 1024),
		topic:   topic,
	}

	go func() {
		for {
			time.Sleep(1 * time.Second)

			msg, ok := <-kafkaSinkInst.msgChan
			if !ok {
				break
			}

			numMessages := uint64(1)

			// Batch messages
		outer:
			for {
				if len(msg) >= 256*1024 {
					break outer
				}
				select {
				case nextMsg, ok := <-kafkaSinkInst.msgChan:
					if ok {
						msg = append(msg, nextMsg...)
						numMessages++
					} else {
						break outer
					}
				default:
					break outer
				}
			}

			for {
				_, _, err := producerInst.SendMessage(&sarama.ProducerMessage{
					Topic: topic,
					Key:   sarama.StringEncoder("default"),
					Value: sarama.ByteEncoder(msg),
				})

				// In addition to kafka client internal retry
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%s] kafka producer send message error: %+v, retrying in 10 seconds\n", time.Now().Format(time.RFC3339), err)
					time.Sleep(10 * time.Second)
				} else {
					break
				}
			}

			kafkaSinkInst.completedMessages.Add(numMessages)
			TotalCompletedMessages.Add(numMessages)

		}
	}()
	return kafkaSinkInst
}

// InitKafkaSink  create kafka sink instance
func InitKafkaSink(u *url.URL) (zap.Sink, error) {
	topic := ""
	if t := u.Query().Get("topic"); len(t) > 0 {
		topic = t
	} else {
		return nil, errors.New("kafka sink topic is empty")
	}
	brokers := []string{u.Host}
	config := sarama.NewConfig()

	// needed for SyncProducer
	config.Producer.Return.Successes = true

	// do not block when started
	config.Metadata.Full = false

	if ack := u.Query().Get("acks"); len(ack) > 0 {
		if iack, err := strconv.Atoi(ack); err == nil {
			config.Producer.RequiredAcks = sarama.RequiredAcks(iack)
		} else {
			fmt.Fprintf(os.Stderr, "kafka producer acks value '%s' invalid  use default value %d\n", ack, config.Producer.RequiredAcks)
		}
	}
	if retries := u.Query().Get("retries"); len(retries) > 0 {
		if iretries, err := strconv.Atoi(retries); err == nil {
			config.Producer.Retry.Max = iretries
		} else {
			fmt.Fprintf(os.Stderr, "kafka producer retries value '%s' invalid  use default value %d\n", retries, config.Producer.Retry.Max)
		}
	}
	config.Net.SASL.Enable = true
	config.Net.SASL.Mechanism = sarama.SASLTypeSCRAMSHA256
	config.Net.SASL.User = u.User.Username()
	config.Net.SASL.Password, _ = u.User.Password()
	config.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { return &XDGSCRAMClient{HashGeneratorFcn: SHA256} }
	config.Net.TLS.Enable = true

	return getKafkaSink(brokers, topic, config), nil
}

// Close implement zap.Sink func Close
func (p *kafkaSink) Close() error {
	close(p.msgChan)
	return nil
}

// Write implement zap.Sink func Write
func (p *kafkaSink) Write(b []byte) (n int, err error) {
	select {
	case p.msgChan <- append([]byte{}, b...):
		p.issuedMessages.Add(1)
		return len(b), nil
	default:
		return 0, errors.New("kafka producer send message queue is full")
	}
}

// Sync implement zap.Sink func Sync
func (p *kafkaSink) Sync() error {
	currentIssuedMessages := p.issuedMessages.Load()
	for p.completedMessages.Load() < currentIssuedMessages {
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}
