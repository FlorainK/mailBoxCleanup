package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"strings"
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
	Raw    []byte
}

type UnpackedMailDTO struct {
	DateSent time.Time
	Sender   string
	Body     string
	Raw      []byte
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
			Raw:    raw,
			Reader: mr,
			SeqNum: msg.SeqNum,
		}
	}
}

func unpackEmailContent(in <-chan EmailStreamDTO, out chan<- UnpackedMailDTO) {
	go func() {
		for msg := range in {
			h := msg.Reader.Header

			dateSent, err := h.Date()
			if err != nil {
				dateSent = time.Time{}
			}

			sender := ""
			fromList, err := h.AddressList("From")
			if err == nil && len(fromList) > 0 {
				if fromList[0].Address != "" {
					sender = fromList[0].Address
				} else {
					sender = fromList[0].String()
				}
			}

			subject, _ := h.Text("Subject")
			log.Printf("Seq %d | Subject: %s", msg.SeqNum, subject)

			var plain strings.Builder
			var html strings.Builder

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
					mediaType, _, _ := h.ContentType()

					b, err := io.ReadAll(part.Body)
					if err != nil {
						log.Printf("failed to read inline body: %v", err)
						continue
					}

					switch strings.ToLower(mediaType) {
					case "text/html":
						html.Write(b)
					case "text/plain":
						plain.Write(b)
					default:
						plain.Write(b)
					}
				}
			}
			fullText := plain.String()
			fullHTML := html.String()

			bestBody := fullText
			if strings.TrimSpace(bestBody) == "" {
				bestBody = fullHTML
			}

			out <- UnpackedMailDTO{
				DateSent: dateSent,
				Sender:   sender,
				Body:     bestBody,
				Raw:      msg.Raw,
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

	emailStreamChannel := make(chan EmailStreamDTO, 50)
	unpackedEmailStreamChannel := make(chan UnpackedMailDTO, 50)

	go fetchInboxEmails(c, emailStreamChannel)
	go unpackEmailContent(emailStreamChannel, unpackedEmailStreamChannel)

	// next steps for the pipeline:
	// classify whether to keep, delete
	// save as file
	// save id as checked
	// delete the email

	time.Sleep(time.Minute)
}
