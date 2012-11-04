package logsync

import (
	"bytes"
	"code.google.com/p/go-imap/go1/imap"
	"fmt"
	"log"
	"mime/multipart"
	"net/mail"
	"os"
	"regexp"
	"time"
)

type chatSyncClient struct {
	client   *imap.Client
	messages map[uint32]*message
	done     chan (uint32)
}

type message struct {
	seq     uint32
	headers *mail.Message
	body    *mail.Message
	done    bool
}

func checkError(err error) {
	if err != nil {
		panic(err)
	}
}

func writeLog(addr, body string, date time.Time) error {
	err := os.Mkdir(addr, 0700)
	if err != nil && !os.IsExist(err) {
		checkError(err)
	}

	path := fmt.Sprintf("%v/%v.html", addr, date.Format("2006-01-02.150405-0700MST"))
	file, err := os.Create(path)

	file.WriteString(body)
	file.Sync()

	return nil
}

func (m *message) process() error {
	// chat recipient
	addrs, _ := m.headers.Header.AddressList("From")
	addr := addrs[0].Address
	//log.Printf("%d * FROM: %v", m.seq, addr)

	// date
	date, _ := m.headers.Header.Date()
	date = date.Local()
	//log.Printf("%d * DATE: %v", m.seq, date)

	// multipart boundary
	contentType := m.headers.Header.Get("Content-Type")
	boundaryRegexp, _ := regexp.Compile(`boundary="(.*)"`)
	boundary := boundaryRegexp.FindStringSubmatch(contentType)[1]
	//log.Printf("%d * BOUNDARY: %v", m.seq, boundary)

	// HTML
	mimeReader := multipart.NewReader(m.body.Body, boundary)
	mimeReader.NextPart() // skip the XML part
	html, _ := mimeReader.NextPart()
	buf := new(bytes.Buffer)
	buf.ReadFrom(html)
	body := buf.String()
	//log.Printf("%d * HTML: %v", m.seq, body)

	err := writeLog(addr, body, date)
	checkError(err)

	m.done = true
	return nil
}

func getImapClient(server string) *chatSyncClient {
	log.Printf("Connecting to IMAP server %v...", server)
	client, err := imap.DialTLS(server, nil)
	checkError(err)

	return &chatSyncClient{client, nil, make(chan uint32)}
}

func (c *chatSyncClient) prepare(username, password, mailbox string) {
	err := os.Mkdir(username, 0700)
	if err != nil && !os.IsExist(err) {
		checkError(err)
	}

	os.Chdir(username)

	// If not logged in, log in
	if c.client.State() == imap.Login {
		log.Print("Logging in...")
		c.client.Login(username, password)
	}

	// Select Chats mailbox
	log.Printf("Selecing mailbox %v...", mailbox)
	c.client.Select(mailbox, true)
	c.messages = make(map[uint32]*message)
}

func (c *chatSyncClient) getMessage(seq uint32) *message {
	result, ok := c.messages[seq]
	if !ok {
		c.messages[seq] = &message{seq, nil, nil, false}
		result = c.messages[seq]
	}
	return result
}

func (c *chatSyncClient) processChat(resp *imap.Response) {
	msgInfo := resp.MessageInfo()
	message := c.getMessage(msgInfo.Seq)

	headerBytes := msgInfo.Attrs["RFC822.HEADER"]
	headers := imap.AsBytes(headerBytes)
	message.headers, _ = mail.ReadMessage(bytes.NewReader(headers))

	bodyBytes := msgInfo.Attrs["BODY[TEXT]"]
	body := imap.AsBytes(bodyBytes)
	message.body, _ = mail.ReadMessage(bytes.NewReader(body))

	err := message.process()
	checkError(err)

	c.done <- message.seq
}

func (c *chatSyncClient) syncChats() error {
	log.Print("Starting sync...")

	// Create SeqSet specifying all messages
	set, err := imap.NewSeqSet("10:20")
	checkError(err)

	// Fetch all messages
	cmd, err := c.client.Fetch(set, "RFC822.HEADER", "BODY[TEXT]")
	checkError(err)

	for cmd.InProgress() {
		c.client.Recv(-1)

		for _, resp := range cmd.Data {
			go c.processChat(resp)
		}

		cmd.Data = nil
	}

	for {
		completed := <-c.done
		delete(c.messages, completed)

		allDone := true
		for k, _ := range c.messages {
			if !c.messages[k].done {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
	}

	log.Print("Processed all chats!")
	return nil
}

func Sync(server, username, password, mailbox string) {
	c := getImapClient(server)
	defer func() {
		r := recover()
		if r != nil {
			log.Print(r)
		}

		log.Print("Closing client...")
		c.client.Logout(30 * time.Second)
	}()

	c.prepare(username, password, mailbox)
	c.syncChats()
}
