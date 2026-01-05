package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/joho/godotenv"
)

type EmailFetchDTO struct {
	fetchMessageData *imapclient.FetchMessageData
	fetchCommand     *imapclient.FetchCommand
}

type EmailContentDTO struct {
}

func fetchInboxEmails(connection *imapclient.Client, outChannel chan<- *imapclient.FetchMessageBuffer) {
	selectedMbox, err := connection.Select("INBOX", nil).Wait()
	if err != nil {
		log.Fatal(err)
	}

	if selectedMbox.NumMessages < 1 {
		return
	}

	log.Printf("Selected inbox contains %v messages", selectedMbox.NumMessages)

	seqSet := imap.SeqSetNum(1)
	fetchOptions := &imap.FetchOptions{
		Envelope: true,
		BodySection: []*imap.FetchItemBodySection{
			{},
		},
	}
	fetchCommand := connection.Fetch(seqSet, fetchOptions)
	go func() {
		defer close(outChannel)
		for {
			fetchMessageData := fetchCommand.Next()
			if fetchMessageData == nil {
				break
			}
			if buffer, err := fetchMessageData.Collect(); err != nil {
				log.Fatal(err)
			} else {
				outChannel <- buffer
			}
			log.Printf("successfully attached email dto to out channel")
		}
		if err = fetchCommand.Close(); err != nil {
			log.Fatal(err)
		}
	}()
}

func unpackInboxEmail(inChannel <-chan *imapclient.FetchMessageBuffer) {
	go func() {
		for buffer := range inChannel {
			fmt.Println("received a message")
			fmt.Println("Subject: ", buffer.Envelope.Subject)

		}
	}()
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	connection, err := imapclient.DialTLS("imap.gmx.com:993", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()

	username := os.Getenv("EMAIL")
	password := os.Getenv("EMAIL_PASSWORD")
	if err = connection.Login(username, password).Wait(); err != nil {
		log.Fatal(err)
	}

	log.Println("Logged in")

	// 2. fetch all emails in inbox
	emailMessageDataChannel := make(chan *imapclient.FetchMessageBuffer, 50)
	go fetchInboxEmails(connection, emailMessageDataChannel)
	go unpackInboxEmail(emailMessageDataChannel)
	time.Sleep(time.Second * 60)
}

// steps:
// 3. classify keep or delete
// 4. delete and backup or continue as is
