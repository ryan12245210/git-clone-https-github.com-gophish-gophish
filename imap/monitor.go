package imap

/* TODO:
*		 - Have a counter per config for number of consecutive login errors and backoff (e.g if supplied creds are incorrect)
*		 - Have a DB field "last_login_error" if last login failed
*		 - DB counter for non-campaign emails that the admin should investigate
*		 - Add field to User for numner of non-campaign emails reported
 */
import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	log "github.com/gophish/gophish/logger"

	"github.com/gophish/gophish/models"
)

// Pattern for GoPhish emails e.g ?rid=AbC123
var goPhishRegex = regexp.MustCompile("(\\?rid=[A-Za-z0-9]{7})")

// Monitor is a worker that monitors IMAP servers for reported campaign emails
type Monitor struct {
	cancel func()
}

// Monitor.start() checks for campaign emails
// As each account can have its own polling frequency set we need to run one Go routine for
// each, as well as keeping an eye on newly created user accounts.
func (im *Monitor) start(ctx context.Context) {

	usermap := make(map[int64]int) // Keep track of running go routines, one per user. We assume incrementing non-repeating UIDs (for the case where users are deleted and re-added).

	for {
		select {
		case <-ctx.Done():
			return
		default:
			dbusers, err := models.GetUsers() //Slice of all user ids. Each user gets their own IMAP monitor routine.
			if err != nil {
				log.Error(err)
				break
			}
			for _, dbuser := range dbusers {
				if _, ok := usermap[dbuser.Id]; !ok { // If we don't currently have a running Go routine for this user, start one.
					log.Info("Starting new IMAP monitor for user ", dbuser.Username)
					usermap[dbuser.Id] = 1
					go monitor(dbuser.Id, ctx)
				}
			}
			time.Sleep(10 * time.Second) // Every ten seconds we check if a new user has been created
		}
	}
}

// monitor will continuously login to the IMAP settings associated to the supplied user id (if the user account has IMAP settings, and they're enabled.)
// It also verifies the user account exists, and returns if not (for the case of a user being deleted).
func monitor(uid int64, ctx context.Context) {

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// 1. Check if user exists, if not, return.
			_, err := models.GetUser(uid)
			if err != nil { // Not sure if there's a better way to determine user existence via id.
				log.Info("User ", uid, " seems to have been deleted. Stopping IMAP monitor for this user.")
				return
			}
			// 2. Check if user has IMAP settings.
			imapSettings, err := models.GetIMAP(uid)
			if err != nil {
				log.Error(err)
				break
			}
			if len(imapSettings) > 0 {
				im := imapSettings[0]
				// 3. Check if IMAP is enabled
				if im.Enabled {
					log.Debug("Checking IMAP for user ", uid, ": ", im.Username, "@", im.Host)
					checkForNewEmails(im)
					time.Sleep((time.Duration(im.IMAPFreq) - 10) * time.Second) // Subtract 10 to compensate for the default sleep of 10 at the bottom
				}
			}
		}
		time.Sleep(10 * time.Second)
	}
}

// NewMonitor returns a new instance of imap.Monitor
func NewMonitor() *Monitor {

	im := &Monitor{}
	return im
}

// Start launches the IMAP campaign monitor
func (im *Monitor) Start() error {
	log.Info("Starting IMAP monitor manager")
	ctx, cancel := context.WithCancel(context.Background()) // ctx is the derivedContext
	im.cancel = cancel
	go im.start(ctx)
	return nil
}

// Shutdown attempts to gracefully shutdown the IMAP monitor.
func (im *Monitor) Shutdown() error {
	log.Info("Shutting down IMAP monitor manager")
	im.cancel()
	return nil
}

// checkForNewEmails logs into an IMAP account and checks unread emails
//  for the rid campaign identifier.
func checkForNewEmails(im models.IMAP) {

	im.Host = im.Host + ":" + strconv.Itoa(int(im.Port)) // Append port
	mailSettings := MailboxInfo{
		Host:   im.Host,
		TLS:    im.TLS,
		User:   im.Username,
		Pwd:    im.Password,
		Folder: im.Folder}

	msgs, err := GetUnread(mailSettings, true, false)
	if err != nil {
		log.Error(err)
		return
	}
	// Update last_succesful_login here via im.Host
	err = models.SuccessfulLogin(&im)

	if len(msgs) > 0 {
		var reportingFailed []uint32 // UIDs of emails that were unable to be reported to phishing server, mark as unread
		var campaignEmails []uint32  // UIDs of campaign emails. If DeleteReportedCampaignEmail is true, we will delete these
		for _, m := range msgs {
			// Check if sender is from company's domain, if enabled. TODO: Make this an IMAP filter
			if im.RestrictDomain != "" { // e.g domainResitct = widgets.com
				splitEmail := strings.Split(m.Email.From, "@")
				senderDomain := splitEmail[len(splitEmail)-1]
				if senderDomain != im.RestrictDomain {
					log.Debug("Ignoring email as not from company domain: ", senderDomain)
					continue
				}
			}

			body := string(append(m.Email.Text, m.Email.HTML...)) // Not sure if we need to check the Text as well as the HTML. Perhaps sometimes Text only emails won't have an HTML component?
			rid := goPhishRegex.FindString(body)

			if rid != "" {
				rid = rid[5:]
				log.Infof("User '%s' reported email with rid %s", m.Email.From, rid)
				result, err := models.GetResult(rid)
				if err != nil {
					log.Error("Error reporting GoPhish email with rid ", rid, ": ", err.Error())
					reportingFailed = append(reportingFailed, m.UID)
				} else {
					err = result.HandleEmailReport(models.EventDetails{})
					if err != nil {
						log.Error("Error updating GoPhish email with rid ", rid, ": ", err.Error())
					} else {
						if im.DeleteReportedCampaignEmail == true {
							campaignEmails = append(campaignEmails, m.UID)
						}
					}
				}
			} else {
				// In the future this should be an alert in Gophish
				log.Debugf("User '%s' reported email with subject '%s'. This is not a GoPhish campaign; you should investigate it.\n", m.Email.From, m.Email.Subject)
			}
			// Check if any emails were unable to be reported, so we can mark them as unread
			if len(reportingFailed) > 0 {
				log.Debugf("Marking %d emails as unread as failed to report\n", len(reportingFailed))
				err := MarkAsUnread(mailSettings, reportingFailed) // Set emails as unread that we failed to report to GoPhish
				if err != nil {
					log.Error("Unable to mark emails as unread: ", err.Error())
				}
			}
			// If the DeleteReportedCampaignEmail flag is set, delete reported Gophish campaign emails
			if im.DeleteReportedCampaignEmail == true && len(campaignEmails) > 0 {
				log.Debugf("Deleting %d campaign emails\n", len(campaignEmails))
				err := DeleteEmails(mailSettings, campaignEmails) // Delete GoPhish campaign emails.
				if err != nil {
					log.Error("Failed to delete emails: ", err.Error())
				}
			}
		}
	} else {
		log.Debug("No new emails for ", im.Username)
	}
}
