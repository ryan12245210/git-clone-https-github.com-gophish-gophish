package imap

// Functionality taken from https://github.com/jprobinson/eazye

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"strconv"
	"time"

	log "github.com/gophish/gophish/logger"
	"github.com/gophish/gophish/models"
	"github.com/jordan-wright/email"
	"github.com/mxk/go-imap/imap"
)

// Email Email struct with IMAP UID
type Email struct {
	UID uint32 `json:"uid"`
	*email.Email
}

// MailboxInfo holds onto the credentials and other information
// needed for connecting to an IMAP server.
type MailboxInfo struct {
	Host   string
	TLS    bool
	User   string
	Pwd    string
	Folder string
	// Read only mode, false (original logic) if not initialized
	ReadOnly bool
}

// GetAll will pull all emails from the email folder and return them as a list.
func GetAll(info MailboxInfo, markAsRead, delete bool) ([]Email, error) {
	// call chan, put 'em in a list, return
	var emails []Email
	responses, err := GenerateAll(info, markAsRead, delete)
	if err != nil {
		return emails, err
	}

	for resp := range responses {
		if resp.Err != nil {
			return emails, resp.Err
		}
		emails = append(emails, resp.Email)
	}

	return emails, nil
}

// GenerateAll will find all emails in the email folder and pass them along to the responses channel.
func GenerateAll(info MailboxInfo, markAsRead, delete bool) (chan Response, error) {
	return generateMail(info, "ALL", nil, markAsRead, delete)
}

// GetUnread will find all unread emails in the folder and return them as a list.
func GetUnread(info MailboxInfo, markAsRead, delete bool) ([]Email, error) {
	// call chan, put 'em in a list, return
	var emails []Email

	responses, err := GenerateUnread(info, markAsRead, delete)
	if err != nil {
		return emails, err
	}

	for resp := range responses {
		if resp.Err != nil {
			return emails, resp.Err
		}
		emails = append(emails, resp.Email)
	}

	return emails, nil
}

// GenerateUnread will find all unread emails in the folder and pass them along to the responses channel.
func GenerateUnread(info MailboxInfo, markAsRead, delete bool) (chan Response, error) {
	return generateMail(info, "UNSEEN", nil, markAsRead, delete)
}

// MarkAsUnread will set the UNSEEN flag on a supplied slice of UIDs
func MarkAsUnread(info MailboxInfo, uids []uint32) error {

	client, err := newClient(info)
	if err != nil {
		return err
	}
	defer func() {
		client.Close(true)
		client.Logout(30 * time.Second)
	}()
	for _, u := range uids {
		err := alterEmail(client, u, "\\SEEN", false)
		if err != nil {
			return err //return on first failure
		}
	}
	return nil

}

// DeleteEmails will delete emails from the supplied slice of UIDs
func DeleteEmails(info MailboxInfo, uids []uint32) error {

	client, err := newClient(info)
	if err != nil {
		return err
	}
	defer func() {
		client.Close(true)
		client.Logout(30 * time.Second)
	}()
	for _, u := range uids {
		err := deleteEmail(client, u)
		if err != nil {
			return err //return on first failure
		}
	}
	return nil

}

// Validate validates supplied IMAP model by connecting to the server
func Validate(s *models.IMAP) error {

	err := s.Validate()
	if err != nil {
		log.Error(err)
		return err
	}

	s.Host = s.Host + ":" + strconv.Itoa(int(s.Port)) // Append port
	mailSettings := MailboxInfo{
		Host:   s.Host,
		TLS:    s.TLS,
		User:   s.Username,
		Pwd:    s.Password,
		Folder: s.Folder}

	client, err := newClient(mailSettings)
	if err != nil {
		log.Error(err.Error())
	} else {
		client.Close(true)
		client.Logout(30 * time.Second)
	}
	return err
}

// Response is a helper struct to wrap the email responses and possible errors.
type Response struct {
	Email Email
	Err   error
}

// newClient will initiate a new IMAP connection with the given creds.
func newClient(info MailboxInfo) (*imap.Client, error) {
	var client *imap.Client
	var err error
	if info.TLS {
		client, err = imap.DialTLS(info.Host, new(tls.Config))
		if err != nil {
			return client, err
		}
	} else {
		client, err = imap.Dial(info.Host)
		if err != nil {
			return client, err
		}
	}

	_, err = client.Login(info.User, info.Pwd)
	if err != nil {
		return client, err
	}

	_, err = imap.Wait(client.Select(info.Folder, info.ReadOnly))
	if err != nil {
		return client, err
	}

	return client, nil
}

const dateFormat = "02-Jan-2006"

// findEmails will run a find the UIDs of any emails that match the search.:
func findEmails(client *imap.Client, search string, since *time.Time) (*imap.Command, error) {
	var specs []imap.Field
	if len(search) > 0 {
		specs = append(specs, search)
	}

	if since != nil {
		sinceStr := since.Format(dateFormat)
		specs = append(specs, "SINCE", sinceStr)
	}

	// get headers and UID for UnSeen message in src inbox...
	cmd, err := imap.Wait(client.UIDSearch(specs...))
	if err != nil {
		return &imap.Command{}, fmt.Errorf("uid search failed: %s", err)
	}
	return cmd, nil
}

var GenerateBufferSize = 100

func generateMail(info MailboxInfo, search string, since *time.Time, markAsRead, delete bool) (chan Response, error) {
	responses := make(chan Response, GenerateBufferSize)
	client, err := newClient(info)
	if err != nil {
		close(responses)
		return responses, fmt.Errorf("failed to create IMAP connection: %s", err)
	}

	go func() {
		defer func() {
			client.Close(true)
			client.Logout(30 * time.Second)
			close(responses)
		}()

		var cmd *imap.Command
		// find all the UIDs
		cmd, err = findEmails(client, search, since)
		if err != nil {
			responses <- Response{Err: err}
			return
		}
		// gotta fetch 'em all
		getEmails(client, cmd, markAsRead, delete, responses)
	}()

	return responses, nil
}

func getEmails(client *imap.Client, cmd *imap.Command, markAsRead, delete bool, responses chan Response) {
	seq := &imap.SeqSet{}
	msgCount := 0
	for _, rsp := range cmd.Data {
		for _, uid := range rsp.SearchResults() {
			msgCount++
			seq.AddNum(uid)
		}
	}

	// nothing to request?! why you even callin me, foolio?
	if seq.Empty() {
		return
	}

	fCmd, err := imap.Wait(client.UIDFetch(seq, "INTERNALDATE", "BODY[]", "UID", "RFC822.HEADER"))
	if err != nil {
		responses <- Response{Err: fmt.Errorf("unable to perform uid fetch: %s", err)}
		return
	}

	var email Email
	for _, msgData := range fCmd.Data {
		msgFields := msgData.MessageInfo().Attrs

		// make sure is a legit response before we attempt to parse it
		// deal with unsolicited FETCH responses containing only flags
		// I'm lookin' at YOU, Gmail!
		// http://mailman13.u.washington.edu/pipermail/imap-protocol/2014-October/002355.html
		// http://stackoverflow.com/questions/26262472/gmail-imap-is-sometimes-returning-bad-results-for-fetch
		if _, ok := msgFields["RFC822.HEADER"]; !ok {
			continue
		}

		email, err = NewEmail(msgFields)
		if err != nil {
			responses <- Response{Err: fmt.Errorf("unable to parse email: %s", err)}
			return
		}

		responses <- Response{Email: email}

		if !markAsRead {
			err = removeSeen(client, imap.AsNumber(msgFields["UID"]))
			if err != nil {
				responses <- Response{Err: fmt.Errorf("unable to remove seen flag: %s", err)}
				return
			}
		}

		if delete {
			err = deleteEmail(client, imap.AsNumber(msgFields["UID"]))
			if err != nil {
				responses <- Response{Err: fmt.Errorf("unable to delete email: %s", err)}
				return
			}
		}
	}
	return
}

func deleteEmail(client *imap.Client, UID uint32) error {
	return alterEmail(client, UID, "\\DELETED", true)
}

func removeSeen(client *imap.Client, UID uint32) error {
	return alterEmail(client, UID, "\\SEEN", false)
}

func alterEmail(client *imap.Client, UID uint32, flag string, plus bool) error {
	flg := "-FLAGS"
	if plus {
		flg = "+FLAGS"
	}
	fSeq := &imap.SeqSet{}
	fSeq.AddNum(UID)
	_, err := imap.Wait(client.UIDStore(fSeq, flg, flag))
	if err != nil {
		return err
	}

	return nil
}

// NewEmail will parse an imap.FieldMap into an Email. This
// will expect the message to container the internaldate and the body with
// all headers included.
func NewEmail(msgFields imap.FieldMap) (Email, error) {

	rawBody := imap.AsBytes(msgFields["BODY[]"])

	rawBodyStream := bytes.NewReader(rawBody)
	em, err := email.NewEmailFromReader(rawBodyStream) // Parse with @jordanwright's library
	if err != nil {
		log.Error("Unable to parse email")
	}
	iem := Email{
		Email: em,
		UID:   imap.AsNumber(msgFields["UID"]),
	}

	return iem, err
}
