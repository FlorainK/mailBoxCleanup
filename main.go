package main

import (
	"errors"
	"log"
	"os"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/joho/godotenv"
)

type mail_connector struct {
	connection *imapclient.Client
}

func (mc *mail_connector) get_folder_messages(folder_name string) error {
	if mc == nil {
		return errors.New("receiver must not be novalue")
	}

	// mc.connection.Fetch()
	return nil
}

func get_mail_connector() (*mail_connector, error) {
	connection, err := imapclient.DialTLS("imap.gmx.com:993", nil)
	if err != nil {
		return nil, err
	}
	username := os.Getenv("EMAIL")
	password := os.Getenv("EMAIL_PASSWORD")
	loginCommand := connection.Login(username, password)
	if err = loginCommand.Wait(); err != nil {
		return nil, err
	}
	log.Println("Logged in")
	mailConnector := mail_connector{connection: connection}

	return &mailConnector, nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	mail_connector, err := get_mail_connector()
	if err != nil {
		log.Fatal(err)
	}

	err = mail_connector.get_folder_messages("INBOX")
	if err != nil {
		log.Fatal(err)
	}

}

// steps:
// 2. fetch all emails in inbox
// 3. classify keep or delete
// 4. delete and backup or continue as is
