// Gmail Mail Mover
// by Dusty Wilson, SCJ Alliance, 2013
// http://code.scjalliance.com/gmail-mail-mover
// License is embedded in this source code and as well as attached in a separate LICENSE file.
// The license within this source code takes precedence where there is conflict.

package main

import (
	"code.google.com/p/go-imap/go1/imap"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"regexp"
	"strings"
	"time"
)

type IMAPClient struct {
	*imap.Client
}

type IMAPAddr string

type Account struct {
	Username   string      `json:"username"`
	Password   string      `json:"password"`
	IMAPAddr   IMAPAddr    `json:"imapaddr"`
	Connection *IMAPClient `json:"-"`
}

type AccountPair struct {
	Main    *Account `json:"main"`
	Archive *Account `json:"archive"`
}

type Config struct {
	AccountPair *AccountPair `json:"accounts"`
	SearchQuery string       `json:"query"`
	MaxMessages int          `json:"max"`
	DryRun      bool         `json:"dryrun"`
}

var config *Config = new(Config)

var ProgramName = "Gmail-Mail-Mover/1.0; http://code.scjalliance.com/gmail-mail-mover"
var LabelWasArchived = "Server/Automated Archival"

var License = `
Copyright (c) 2013 Dusty Wilson.  All rights reserved.
Copyright (c) 2013 SCJ Alliance.  All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

 * Redistributions of source code must retain the above copyright
   notice, this list of conditions and the following disclaimer.

 * Redistributions in binary form must reproduce the above copyright
   notice, this list of conditions and the following disclaimer in the
   documentation and/or other materials provided with the
   distribution.

 * Neither the name of the Gmail-Mail-Mover project nor the names of
   its contributors may be used to endorse or promote products derived
   from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
`

// what shall we log to STDOUT?
var debugMask = imap.LogNone

var MessageIsArchivePlaceholderMatch = regexp.MustCompile("(?im)^X-SCJMAILARCHIVE:")
var MessageIdMatch = regexp.MustCompile(`(?im)^Message-ID:\s*(\S+)`)
var MessageContentTypeMatch = regexp.MustCompile(`(?im)^Content-Type:\s*(.+?)(\r?\n\s+.+?)?$`)
var SubjectMatch = regexp.MustCompile(`(?im)^Subject:[ \t]*(\S[^\r\n]+)`)

func main() {
	fmt.Println(License)
	fmt.Println(ProgramName)
	fmt.Println()

	configFileName := "gmail-mail-mover.conf"
	if len(os.Args) > 1 {
		configFileName = os.Args[1]
	}
	configFile, err := os.Open(configFileName)
	if err != nil {
		fmt.Printf("The configuration file is missing.  Either specify a configuration file or configure %s.\n", configFileName)
		fmt.Printf("Usage:  %s [configFileName]\n", os.Args[0])
		os.Exit(1)
	}
	configDecoder := json.NewDecoder(configFile)
	err = configDecoder.Decode(config)
	if err != nil {
		fmt.Println("[CONFIG FILE FORMAT ERROR] " + err.Error())
		fmt.Println("Please ensure that your config file is in valid JSON format.")
		os.Exit(1)
	}

	boxesCreated := make(map[string]bool)

	ap := config.AccountPair
	if ap.Main.IMAPAddr == "" {
		ap.Main.IMAPAddr = "imap.gmail.com:993"
	}
	if ap.Archive.IMAPAddr == "" {
		ap.Archive.IMAPAddr = "imap.gmail.com:993"
	}

	defer ap.Logout()
	fmt.Println("Connecting to IMAP servers...")
	ap.Connect()

	if ap.Main == nil || ap.Main.Connection == nil {
		panic("NOT CONNECTED TO MAIN ACCOUNT")
	}
	if ap.Archive == nil || ap.Archive.Connection == nil {
		panic("NOT CONNECTED TO ARCHIVE ACCOUNT")
	}

	imap.Wait(ap.Main.Connection.Create(LabelWasArchived)) // create the "was archived" label on the main account

	_, err = ap.Main.Connection.Select("[Gmail]/All Mail", false)
	if err != nil {
		panic(err)
	}

	_, err = ap.Archive.Connection.Select("[Gmail]/All Mail", false)
	if err != nil {
		panic(err)
	}

	SearchQuery := config.SearchQuery

	fmt.Printf("Searching for messages in [%s] that match \"%s\"...\n", ap.Main.Username, SearchQuery)
	cmd, err := imap.Wait(ap.Main.Connection.UIDSearch(`X-GM-RAW`, imap.Quote(SearchQuery, true)))
	if err != nil {
		panic(err)
	}
	if len(cmd.Data) == 0 {
		panic("Unexpected empty data set.")
	}
	messagesMatched := len(cmd.Data[0].SearchResults())
	if messagesMatched == 0 {
		fmt.Println("No messages matched the query.  Quitting.")
		os.Exit(0)
	}
	fmt.Printf("Messages matched query: %d.\n", messagesMatched)
	if messagesMatched > config.MaxMessages {
		fmt.Printf("Limiting to max messages count of: %d.\n", config.MaxMessages)
		messagesMatched = config.MaxMessages
	}
	for i, messageIndex := range cmd.Data[0].SearchResults() {
		i1 := i + 1

		if i1 > messagesMatched {
			break
		}

		resultSet, _ := imap.NewSeqSet("")
		resultSet.AddNum(messageIndex)
		fmt.Printf("Getting information about message %d of %d from [%s]...\n", i1, messagesMatched, ap.Main.Username)
		cmd, err := imap.Wait(ap.Main.Connection.UIDFetch(resultSet, "X-GM-MSGID", "X-GM-THRID", "X-GM-LABELS", "FLAGS", "INTERNALDATE", "RFC822.SIZE", "BODY.PEEK[HEADER]"))
		if err != nil {
			panic(err)
		}
		if len(cmd.Data) == 0 {
			panic("Unexpected empty data set.")
		}
		messageData := cmd.Data[0]
		if len(messageData.Fields) < 3 {
			panic("Unexpectedly short field set.")
		}
		messageSoloSet, _ := imap.NewSeqSet(fmt.Sprintf("%d", imap.AsNumber(messageData.Fields[0])))
		fieldMap := imap.AsFieldMap(messageData.Fields[2])
		timedate := imap.AsDateTime(fieldMap["INTERNALDATE"])
		messageSize := fmt.Sprintf("%.2f MB", (float32(imap.AsNumber(fieldMap["RFC822.SIZE"])) / 1024 / 1024))

		if MessageIsArchivePlaceholderMatch.MatchString(imap.AsString(fieldMap["BODY[HEADER]"])) {
			fmt.Printf("Skipping message %d because it is a placeholder message.\n", i1)
			continue // we don't want to archive an archived message placeholder
		}

		rfc822msgidSet := MessageIdMatch.FindStringSubmatch(imap.AsString(fieldMap["BODY[HEADER]"]))
		if len(rfc822msgidSet) < 2 { // has to match something...
			fmt.Printf("Skipping message %d because it is missing a valid Message-ID header.\n", i1)
			continue
		}
		rfc822msgid := rfc822msgidSet[1]

		subjectSet := SubjectMatch.FindStringSubmatch(imap.AsString(fieldMap["BODY[HEADER]"]))
		if len(subjectSet) < 2 { // has to match something...
			fmt.Printf("Skipping message %d because it is missing a valid Subject header.\n", i1)
			fmt.Println(imap.AsString(fieldMap["BODY[HEADER]"]))
			continue
		}
		subject := subjectSet[1]
		fmt.Printf("\tMessage %d: [%s]\n\tSUB: [%s]\n\tMID: [%s]\n", i1, messageSize, subject, rfc822msgid)

		if config.DryRun {
			fmt.Println("Skipping to next message since we don't want to actually do work here (dryrun).")
			continue
		}

		fmt.Printf("Downloading %s message %d of %d from [%s]...\n", messageSize, i1, messagesMatched, ap.Main.Username)
		cmd, err = imap.Wait(ap.Main.Connection.UIDFetch(resultSet, "X-GM-MSGID", "X-GM-THRID", "X-GM-LABELS", "FLAGS", "INTERNALDATE", "RFC822.SIZE", "BODY.PEEK[HEADER]", "BODY.PEEK[]"))
		if err != nil {
			panic(err)
		}
		if len(cmd.Data) == 0 {
			panic("Unexpected empty data set.")
		}
		messageData = cmd.Data[0]
		if len(messageData.Fields) < 3 {
			panic("Unexpectedly short field set.")
		}
		fieldMap = imap.AsFieldMap(messageData.Fields[2])

		fmt.Printf("Uploading %s message %d of %d to [%s]...\n", messageSize, i1, messagesMatched, ap.Archive.Username)
		_, err = imap.Wait(ap.Archive.Connection.Append("[Gmail]/All Mail", imap.AsFlagSet(fieldMap["FLAGS"]), &timedate, imap.NewLiteral(imap.AsBytes(fieldMap["BODY[]"])))) // insert into archive account
		if err != nil {
			panic(err)
		}

		fmt.Printf("Locating uploaded message %d in [%s]...\n", i1, ap.Archive.Username)
		newMessageCmd, err := imap.Wait(ap.Archive.Connection.Search(`X-GM-RAW`, imap.Quote(fmt.Sprintf("rfc822msgid:%s", rfc822msgid), true)))
		if err != nil {
			panic(err)
		}
		if len(newMessageCmd.Data) > 0 {
			archivedMessageSet, _ := imap.NewSeqSet("")
			archivedMessageSet.AddNum(newMessageCmd.Data[0].SearchResults()...)

			fmt.Printf("Adding necessary labels to [%s] for message %d...\n", ap.Archive.Username, i1)
			for _, l := range imap.AsList(fieldMap["X-GM-LABELS"]) {
				label := imap.AsString(l)
				if strings.HasPrefix(label, "/") || strings.HasPrefix(label, "\\") {
					continue
				}
				if !boxesCreated[label] { // we haven't tried to create this label yet this session
					fmt.Printf("\tCreating Label: [%s]\n", l)
					imap.Wait(ap.Archive.Connection.Create(label)) // create the label on the archive account
					boxesCreated[label] = true
				}
			}
			fmt.Printf("Attaching labels to message %d in [%s]...\n", i1, ap.Archive.Username)
			_, err = imap.Wait(ap.Archive.Connection.Store(archivedMessageSet, "X-GM-LABELS", fieldMap["X-GM-LABELS"])) // attach the labels to the message
			if err != nil {
				panic(err)
			}

			fmt.Printf("Removing archived message %d from [%s]...\n", i1, ap.Main.Username)
			_, err = imap.Wait(ap.Main.Connection.Copy(messageSoloSet, "[Gmail]/Trash")) // move the original to the trash
			if err != nil {
				panic(err)
			}
			_, err := ap.Main.Connection.Select("[Gmail]/Trash", false) // switch to the trash folder so we can purge this message
			if err != nil {
				panic(err)
			}
			deleteableMessageCmd, err := imap.Wait(ap.Main.Connection.UIDSearch(`X-GM-RAW`, imap.Quote(fmt.Sprintf("rfc822msgid:%s", rfc822msgid), true)))
			if err != nil {
				panic(err)
			}
			if len(deleteableMessageCmd.Data) == 0 {
				panic("Unexpected empty data set.")
			}
			deleteMessageSet, _ := imap.NewSeqSet("")
			deleteMessageSet.AddNum(deleteableMessageCmd.Data[0].SearchResults()...)
			if deleteMessageSet.Empty() {
				panic("Unexpected empty data set.")
			}
			_, err = imap.Wait(ap.Main.Connection.UIDStore(deleteMessageSet, "+FLAGS.SILENT", imap.NewFlagSet(`\Deleted`))) // delete the original from the trash
			if err != nil {
				panic(err)
			}
			_, err = imap.Wait(ap.Main.Connection.Expunge(deleteMessageSet)) // flush the original
			if err != nil {
				panic(err)
			}
			_, err = ap.Main.Connection.Select("[Gmail]/All Mail", false) // back to the "all mail" folder as expected
			if err != nil {
				panic(err)
			}

			// append and update placeholder message
			fmt.Printf("Inserting archive placeholder for message %d in [%s]...\n", i1, ap.Main.Username)
			alteredHeader := MessageContentTypeMatch.ReplaceAllString(imap.AsString(fieldMap["BODY[HEADER]"]), "Content-Type: text/html; charset=UTF-8\r\nX-SCJMAILARCHIVE: true")
			placeholderMessage := fmt.Sprintf("%s\r\n<span style='font-size:larger'><span style='font-size:larger;font-weight:bold'>NOTICE:</span><br/>\r\n<span style='font-weight:bold'>This message was moved to an email archive account via an automated process.</span><br/>\r\n<br/>\r\nAt the time of archival, the destination archive account was:<br/>\r\n<span style='font-family:monospace'>%s</span><br/>\r\n<br/>\r\nIf the archive account is a Gmail account, you may be able to locate the\r\nmessage by searching for this string from within the archive account:<br/>\r\n<span style='font-family:monospace'>rfc822msgid:%s</span><br/>\r\n<br/>\r\nThe query used to select this email for archival was:<br/>\r\n<span style='font-family:monospace'>%s</span><br/>\r\n<br/>\r\nThis archival operation occurred at:<br/>\r\n<span style='font-family:monospace'>%s</span><br/>\r\n<br/>\r\nThis email was archived using:<br/>\r\n<span style='font-family:monospace'>%s</span></span><br/>\r\n", alteredHeader, html.EscapeString(ap.Archive.Username), html.EscapeString(rfc822msgid), html.EscapeString(SearchQuery), html.EscapeString(time.Now().String()), html.EscapeString(ProgramName))
			_, err = imap.Wait(ap.Main.Connection.Append("[Gmail]/All Mail", imap.AsFlagSet(fieldMap["FLAGS"]), &timedate, imap.NewLiteral([]byte(placeholderMessage)))) // insert a "moved this message" placeholder at source location
			if err != nil {
				panic(err)
			}
			placeholderMessageCmd, err := imap.Wait(ap.Main.Connection.UIDSearch(`X-GM-RAW`, imap.Quote(fmt.Sprintf("rfc822msgid:%s", rfc822msgid), true)))
			if err != nil {
				panic(err)
			}
			if len(placeholderMessageCmd.Data) == 0 {
				panic("len(placeholderMessageCmd.Data) == 0")
			}
			placeholderMessageSet, _ := imap.NewSeqSet("")
			placeholderMessageSet.AddNum(placeholderMessageCmd.Data[0].SearchResults()...)
			placeholderFieldMap := make(imap.FlagSet)
			if imap.AsFlagSet(fieldMap["X-GM-LABELS"]) != nil {
				placeholderFieldMap = imap.AsFlagSet(fieldMap["X-GM-LABELS"])
			}
			placeholderFieldMap[imap.Quote(LabelWasArchived, true)] = true
			cmd, err = imap.Wait(ap.Main.Connection.UIDStore(placeholderMessageSet, "X-GM-LABELS", placeholderFieldMap)) // attach the labels to the placeholder message
			if err != nil {
				panic(fmt.Sprintln(cmd, err))
			}

			fmt.Printf("Finished with message %d of %d.\n", i1, messagesMatched)
		}
	}
	fmt.Println("Finished.")
}

func (ap *AccountPair) Connect() {
	if ap.Main != nil {
		ap.Main.Connection = ap.Main.IMAPAddr.Dial()
		if ap.Main.Connection.Caps["STARTTLS"] {
			ap.Main.Connection.StartTLS(nil)
		}
		ap.Main.Connection.SetLogger(log.New(os.Stdout, fmt.Sprintf("[%s]  ", ap.Main.Username), 0))
		ap.Main.Connection.SetLogMask(debugMask)
		if ap.Main.Connection.State() == imap.Login {
			_, err := ap.Main.Connection.Login(ap.Main.Username, ap.Main.Password)
			if err != nil {
				panic(err)
			}
		} else {
			panic(ap.Main.Connection.State())
		}
	}

	if ap.Archive != nil {
		ap.Archive.Connection = ap.Archive.IMAPAddr.Dial()
		if ap.Archive.Connection.Caps["STARTTLS"] {
			ap.Archive.Connection.StartTLS(nil)
		}
		ap.Archive.Connection.SetLogger(log.New(os.Stdout, fmt.Sprintf("[%s]  ", ap.Archive.Username), 0))
		ap.Archive.Connection.SetLogMask(debugMask)
		if ap.Archive.Connection.State() == imap.Login {
			_, err := ap.Archive.Connection.Login(ap.Archive.Username, ap.Archive.Password)
			if err != nil {
				panic(err)
			}
		} else {
			panic(ap.Archive.Connection.State())
		}
	}
}

func (ap *AccountPair) Logout() {
	if ap.Main != nil && ap.Main.Connection != nil {
		ap.Main.Connection.Logout(30 * time.Second)
	}
	if ap.Archive != nil && ap.Archive.Connection != nil {
		ap.Archive.Connection.Logout(30 * time.Second)
	}
}

func (addr IMAPAddr) Dial() *IMAPClient {
	var c *imap.Client
	var err error
	if strings.HasSuffix(string(addr), ":993") {
		c, err = imap.DialTLS(string(addr), nil)
	} else {
		c, err = imap.Dial(string(addr))
	}
	if err != nil {
		panic(err)
	}
	return &IMAPClient{c}
}
