package service

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/resend/resend-go/v2"
)

func sanitizeNotificationSubject(subject string) string {
	var out strings.Builder
	for _, r := range subject {
		if unicode.IsControl(r) || r == '\r' || r == '\n' {
			continue
		}
		out.WriteRune(r)
		if utf8.RuneCountInString(out.String()) >= 140 {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

func randomMIMEBoundary() (string, error) {
	var random [18]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "multica-" + hex.EncodeToString(random[:]), nil
}

func writeQuotedPrintablePart(writer *multipart.Writer, contentType, body string) error {
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              []string{contentType},
		"Content-Transfer-Encoding": []string{"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(part)
	if _, err := qp.Write([]byte(body)); err != nil {
		_ = qp.Close()
		return err
	}
	return qp.Close()
}

// SendNotificationEmail sends the same Careli template through the configured
// SMTP or Resend transport and embeds the supplied logos inline.
func (s *EmailService) SendNotificationEmail(to, subject, htmlBody, textBody string) error {
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("notification recipient is empty")
	}
	assets, err := notificationEmailAssets()
	if err != nil {
		return err
	}
	subject = sanitizeNotificationSubject(subject)
	if s.smtpHost != "" {
		return s.sendSMTPNotification(to, subject, htmlBody, textBody, assets)
	}
	if s.client == nil {
		return fmt.Errorf("email transport is not configured")
	}
	_, err = s.client.Emails.Send(&resend.SendEmailRequest{
		From: s.fromEmail, To: []string{to}, Subject: subject,
		Html: htmlBody, Text: textBody, Attachments: assets,
	})
	return err
}

func (s *EmailService) sendSMTPNotification(to, subject, htmlBody, textBody string, assets []*resend.Attachment) error {
	if strings.TrimSpace(s.fromEmail) == "" {
		return fmt.Errorf("SMTP_FROM_EMAIL or RESEND_FROM_EMAIL is required when SMTP_HOST is set")
	}
	c, err := s.openSMTPClient()
	if err != nil {
		return err
	}
	defer c.Close()
	if s.smtpUsername != "" {
		fallbackToLogin, authErr := smtpAuthWithFallback(smtpClientAdapter{client: c}, s.smtpHost, s.smtpUsername, s.smtpPassword)
		if authErr != nil {
			if !fallbackToLogin {
				return fmt.Errorf("smtp auth: %w", authErr)
			}
			c.Close()
			c, err = s.openSMTPClient()
			if err != nil {
				return fmt.Errorf("smtp auth reconnect failed: %w", err)
			}
			defer c.Close()
			if err = c.Auth(&loginAuth{username: s.smtpUsername, password: s.smtpPassword, host: s.smtpHost}); err != nil {
				return fmt.Errorf("smtp login auth failed: %w", err)
			}
		}
	}

	altBoundary, err := randomMIMEBoundary()
	if err != nil {
		return fmt.Errorf("create MIME boundary: %w", err)
	}
	relatedBoundary, err := randomMIMEBoundary()
	if err != nil {
		return fmt.Errorf("create MIME boundary: %w", err)
	}
	var alternative bytes.Buffer
	alt := multipart.NewWriter(&alternative)
	if err := alt.SetBoundary(altBoundary); err != nil {
		return fmt.Errorf("set alternative MIME boundary: %w", err)
	}
	if err := writeQuotedPrintablePart(alt, "text/plain; charset=UTF-8", textBody); err != nil {
		return fmt.Errorf("write notification text part: %w", err)
	}
	if err := writeQuotedPrintablePart(alt, "text/html; charset=UTF-8", htmlBody); err != nil {
		return fmt.Errorf("write notification HTML part: %w", err)
	}
	if err := alt.Close(); err != nil {
		return fmt.Errorf("close alternative MIME part: %w", err)
	}

	var body bytes.Buffer
	related := multipart.NewWriter(&body)
	if err := related.SetBoundary(relatedBoundary); err != nil {
		return fmt.Errorf("set related MIME boundary: %w", err)
	}
	altPart, err := related.CreatePart(textproto.MIMEHeader{
		"Content-Type": []string{fmt.Sprintf("multipart/alternative; boundary=%q", altBoundary)},
	})
	if err != nil {
		return fmt.Errorf("create alternative MIME part: %w", err)
	}
	if _, err := altPart.Write(alternative.Bytes()); err != nil {
		return fmt.Errorf("write alternative MIME part: %w", err)
	}
	for _, asset := range assets {
		if asset == nil || len(asset.Content) == 0 || asset.ContentId == "" {
			continue
		}
		part, err := related.CreatePart(textproto.MIMEHeader{
			"Content-Type":              []string{asset.ContentType + `; name="` + asset.Filename + `"`},
			"Content-Transfer-Encoding": []string{"base64"},
			"Content-ID":                []string{"<" + asset.ContentId + ">"},
			"Content-Disposition":       []string{`inline; filename="` + asset.Filename + `"`},
		})
		if err != nil {
			return fmt.Errorf("create inline image part: %w", err)
		}
		encoded := base64.NewEncoder(base64.StdEncoding, part)
		if _, err := encoded.Write(asset.Content); err != nil {
			_ = encoded.Close()
			return fmt.Errorf("write inline image part: %w", err)
		}
		if err := encoded.Close(); err != nil {
			return fmt.Errorf("close inline image part: %w", err)
		}
	}
	if err := related.Close(); err != nil {
		return fmt.Errorf("close related MIME part: %w", err)
	}

	if err := c.Mail(s.fromEmail); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	writer, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	messageID, err := randomMIMEBoundary()
	if err != nil {
		_ = writer.Close()
		return fmt.Errorf("create message id: %w", err)
	}
	headers := "From: " + s.fromEmail + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"Date: " + timeNowRFC1123Z() + "\r\n" +
		"Message-ID: <" + messageID + "@multica>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/related; type=\"multipart/alternative\"; boundary=" + relatedBoundary + "\r\n\r\n"
	if _, err := writer.Write([]byte(headers)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write notification headers: %w", err)
	}
	if _, err := writer.Write(body.Bytes()); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write notification body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish notification email: %w", err)
	}
	return c.Quit()
}

var timeNowRFC1123Z = func() string { return time.Now().UTC().Format(time.RFC1123Z) }
