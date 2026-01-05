package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/joho/godotenv"
)

type EmailStreamDTO struct {
	Reader *mail.Reader
	SeqNum uint32
}

func fetchInboxEmails(
	connection *imapclient.Client,
	out chan<- EmailStreamDTO,
) {
	defer close(out)

	mbox, err := connection.Select("INBOX", nil).Wait()
	if err != nil {
		log.Fatal(err)
	}
	if mbox.NumMessages == 0 {
		log.Println("Inbox empty")
		return
	}

	seqSet := imap.SeqSetNum(1)

	bodySection := &imap.FetchItemBodySection{}
	opts := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}

	cmd := connection.Fetch(seqSet, opts)
	defer cmd.Close()

	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}

		var body imapclient.FetchItemDataBodySection
		found := false

		for {
			item := msg.Next()
			if item == nil {
				break
			}
			if bs, ok := item.(imapclient.FetchItemDataBodySection); ok {
				body = bs
				found = true
				break
			}
		}

		if !found {
			log.Println("message without BODY[]")
			continue
		}

		raw, err := io.ReadAll(body.Literal)
		if err != nil {
			log.Printf("failed to read message body: %v", err)
			continue
		}

		mr, err := mail.CreateReader(bytes.NewReader(raw))
		if err != nil {
			log.Printf("failed to create mail reader: %v", err)
			continue
		}

		out <- EmailStreamDTO{
			Reader: mr,
			SeqNum: msg.SeqNum,
		}
	}
}

func unpackInboxEmail(in <-chan EmailStreamDTO) {
	go func() {
		for msg := range in {
			h := msg.Reader.Header

			subject, _ := h.Text("Subject")
			log.Printf("Seq %d | Subject: %s", msg.SeqNum, subject)

			for {
				part, err := msg.Reader.NextPart()
				if err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					log.Fatal(err)
				}

				switch h := part.Header.(type) {
				case *mail.InlineHeader:
					body, _ := io.ReadAll(part.Body)
					log.Printf("Inline text (%d bytes)", len(body))

				case *mail.AttachmentHeader:
					filename, _ := h.Filename()
					log.Printf("Attachment: %s", filename)
				}
			}
		}
	}()
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Fatal(err)
	}

	c, err := imapclient.DialTLS("imap.gmx.com:993", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	if err := c.Login(
		os.Getenv("EMAIL"),
		os.Getenv("EMAIL_PASSWORD"),
	).Wait(); err != nil {
		log.Fatal(err)
	}

	log.Println("Logged in")

	ch := make(chan EmailStreamDTO, 10)

	go fetchInboxEmails(c, ch)
	go unpackInboxEmail(ch)

	time.Sleep(time.Minute)
}
