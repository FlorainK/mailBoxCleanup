package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	_ "github.com/emersion/go-message/charset"
	"github.com/emersion/go-message/mail"
	"github.com/joho/godotenv"
)

var openAIKey string

type EmailStreamDTO struct {
	Reader *mail.Reader
	SeqNum uint32
	UID    imap.UID
	Raw    []byte
}

type UnpackedMailDTO struct {
	SeqNum        uint32
	UID           imap.UID
	DateSent      time.Time
	Sender        string
	Header        string
	Body          string
	Raw           []byte
	Justification string
}

type OpenAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type ClassificationResult struct {
	Classification string `json:"classification"`
	Justification  string `json:"justification"`
}

func fetchInboxEmails(
	connection *imapclient.Client,
	out chan<- EmailStreamDTO,
	wg *sync.WaitGroup,
) {
	defer close(out)
	defer wg.Done()

	mbox, err := connection.Select("INBOX", nil).Wait()
	if err != nil {
		log.Fatal(err)
	}
	if mbox.NumMessages == 0 {
		log.Println("Inbox empty")
		return
	}

	seqSet := imap.SeqSetNum(1)
	seqSet.AddRange(1, 10)

	bodySection := &imap.FetchItemBodySection{}
	opts := &imap.FetchOptions{
		UID:         true,
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
		var uid imap.UID
		foundBody := false
		foundUID := false

		for {
			item := msg.Next()
			if item == nil {
				break
			}
			switch v := item.(type) {
			case imapclient.FetchItemDataBodySection:
				body = v
				foundBody = true
			case imapclient.FetchItemDataUID:
				uid = v.UID
				foundUID = true
			}
		}

		if !foundBody {
			log.Println("message without BODY[]")
			continue
		}
		if !foundUID {
			log.Printf("Seq %d: message without UID", msg.SeqNum)
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
			UID:    uid,
		}
	}
}

func unpackEmailContent(in <-chan EmailStreamDTO, out chan<- UnpackedMailDTO, wg *sync.WaitGroup) {
	go func() {
		defer wg.Done()
		defer close(out)
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
				SeqNum:   msg.SeqNum,
				UID:      msg.UID,
				DateSent: dateSent,
				Sender:   sender,
				Header:   subject,
				Body:     bestBody,
				Raw:      msg.Raw,
			}
		}
	}()
}

func processEmailContent(in <-chan UnpackedMailDTO, deleteChan chan<- UnpackedMailDTO, doneChan chan<- imap.UID, wg *sync.WaitGroup) {
	go func() {
		today := time.Now()
		defer wg.Done()
		for item := range in {
			if today.Sub(item.DateSent).Hours()/24/365 > 3 {
				deleteChan <- item
				continue
			}
			body := strings.NewReader(fmt.Sprintf(`{
			"model": "gpt-5-nano-2025-08-07,
			"messages": 
				[
				"role": "user",
				"content": "
					Task:
					You are my (florian kenner's) personal assistant who is instructed to cleanup his mailbox. Rate the following email, whether to keep or delete it. You want to generally delete all emails which will not be of any importance in the future, such as ads, spam, newsletters outdated information, such as one time authentication codes etc.
					Keep all emails might need follow up in the future, e.g. an upcoming event which is in the future or very recent past, bills, invoices etc.
					Delete emails which are outdated automated notifications, spam or just not relevant anymore. Todays date is %v.
					If you are not totally sure, keep the email.

					Email Title: %v
					Email Sender: %v
					Date: %v
					Email Content: %v


					Format of Expected Output: Please provide your classification and justification in the following structured json format:
						{{
							"classification": "KEEP" or "DELETE",
							"justification": "A one short sentence explanation of why you made this classification based on the provided email"
						}}
						"
				]
			}
			`, today, item.Header, item.Sender, item.DateSent, item.Body))

			req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", body)
			if err != nil {
				log.Fatal(err)
			}
			req.Header.Add("Authorization", fmt.Sprintf("Bearer: %s", openAIKey))
			req.Header.Add("Content-Type", "application/json")

			client := &http.Client{}

			resp, err := client.Do(req)
			if err != nil {
				log.Fatal(err)
			}
			defer resp.Body.Close()

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				log.Fatal(err)
			}

			var response OpenAIResponse
			err = json.Unmarshal(respBody, &response)
			if err != nil {
				log.Fatal(err)
			}

			content := response.Choices[0].Message.Content

			var classificationResult ClassificationResult
			err = json.Unmarshal([]byte(content), &classificationResult)
			if err != nil {
				log.Fatal(err)
			}

			classification := classificationResult.Classification

			if classification == "KEEP" {
				doneChan <- item.UID
				continue
			}

			item.Justification = classificationResult.Justification

			deleteChan <- item

			resp.Body.Close()
		}
	}()
}

func deleteEmail(connection *imapclient.Client, in <-chan UnpackedMailDTO, out chan<- UnpackedMailDTO, wg *sync.WaitGroup) {
	go func() {
		defer close(out)
		defer wg.Done()

		if _, err := connection.Select("INBOX", nil).Wait(); err != nil {
			log.Printf("failed to select INBOX before deletion: %v", err)
			return
		}

		for item := range in {
			if item.UID == 0 {
				log.Printf("Seq %d: cannot delete without UID", item.SeqNum)
				continue
			}

			uidSet := imap.UIDSetNum(item.UID)
			storeCmd := connection.Store(uidSet, &imap.StoreFlags{
				Op:     imap.StoreFlagsAdd,
				Silent: true,
				Flags:  []imap.Flag{imap.FlagDeleted},
			}, nil)
			if err := storeCmd.Close(); err != nil {
				log.Printf("UID %d: failed to set \\\u005cDeleted: %v", item.UID, err)
				continue
			}

			expungeCmd := connection.UIDExpunge(uidSet)
			if _, err := expungeCmd.Collect(); err != nil {
				log.Printf("UID %d: UID EXPUNGE failed (server may not support UIDPLUS/IMAP4rev2): %v", item.UID, err)
				continue
			}

			out <- item
		}
	}()
}

func backupEmail(in <-chan UnpackedMailDTO, out chan<- imap.UID, wg *sync.WaitGroup) {
	go func() {
		defer close(out)
		defer wg.Done()

		if err := os.MkdirAll("./email_backups", os.ModePerm); err != nil {
			log.Fatal(err)
		}

		for msg := range in {
			filename := fmt.Sprintf("./email_backups/%d_%s.eml", msg.UID, strings.ReplaceAll(msg.Header, " ", "_"))
			err := os.WriteFile(filename, msg.Raw, 0644)
			if err != nil {
				log.Printf("UID %d: failed to save email: %v", msg.UID, err)
				continue
			}
			out <- msg.UID
		}
	}()
}

func markEmailAsProcessed(in <-chan imap.UID) {
	f, err := os.OpenFile("checked_emails.txt", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		panic(err)
	}

	defer f.Close()
	for {
		select {
		case id := <-in:

			if _, err = f.WriteString(string(id)); err != nil {
				log.Fatal(err)
			}
		default:
			return
		}
	}

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

	openAIKey = os.Getenv("OPENAI_API_KEY")

	emailStreamChannel := make(chan EmailStreamDTO, 50)
	unpackedEmailStreamChannel := make(chan UnpackedMailDTO, 50)
	var wg sync.WaitGroup
	wg.Add(1)
	go fetchInboxEmails(c, emailStreamChannel, &wg)
	wg.Add(1)
	go unpackEmailContent(emailStreamChannel, unpackedEmailStreamChannel, &wg)
	deleteChannel := make(chan UnpackedMailDTO, 50)
	doneChannel := make(chan imap.UID, 10000)
	wg.Add(1)
	go processEmailContent(unpackedEmailStreamChannel, deleteChannel, doneChannel, &wg)
	wg.Add(1)
	backupChannel := make(chan UnpackedMailDTO, 50)
	go deleteEmail(c, deleteChannel, backupChannel, &wg)
	wg.Add(1)
	go backupEmail(backupChannel, doneChannel, &wg)
	// next steps for the pipeline:
	wg.Wait()

	markEmailAsProcessed(doneChannel)
}
