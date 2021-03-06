package access

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudflare/cloudflared/carrier"
	"github.com/cloudflare/cloudflared/config"
	"github.com/cloudflare/cloudflared/h2mux"
	"github.com/cloudflare/cloudflared/logger"
	"github.com/cloudflare/cloudflared/validation"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/urfave/cli/v2"
)

const (
	LogFieldHost = "host"
)

// StartForwarder starts a client side websocket forward
func StartForwarder(forwarder config.Forwarder, shutdown <-chan struct{}, log *zerolog.Logger) error {
	validURL, err := validation.ValidateUrl(forwarder.Listener)
	if err != nil {
		return errors.Wrap(err, "error validating origin URL")
	}

	// get the headers from the config file and add to the request
	headers := make(http.Header)
	if forwarder.TokenClientID != "" {
		headers.Set(h2mux.CFAccessClientIDHeader, forwarder.TokenClientID)
	}

	if forwarder.TokenSecret != "" {
		headers.Set(h2mux.CFAccessClientSecretHeader, forwarder.TokenSecret)
	}

	if forwarder.Destination != "" {
		headers.Add(h2mux.CFJumpDestinationHeader, forwarder.Destination)
	}

	options := &carrier.StartOptions{
		OriginURL: forwarder.URL,
		Headers:   headers, //TODO: TUN-2688 support custom headers from config file
	}

	// we could add a cmd line variable for this bool if we want the SOCK5 server to be on the client side
	wsConn := carrier.NewWSConnection(log)

	log.Info().Str(LogFieldHost, validURL.Host).Msg("Start Websocket listener")
	return carrier.StartForwarder(wsConn, validURL.Host, shutdown, options)
}

// ssh will start a WS proxy server for server mode
// or copy from stdin/stdout for client mode
// useful for proxying other protocols (like ssh) over websockets
// (which you can put Access in front of)
func ssh(c *cli.Context) error {
	log := logger.CreateSSHLoggerFromContext(c, logger.EnableTerminalLog)

	// get the hostname from the cmdline and error out if its not provided
	rawHostName := c.String(sshHostnameFlag)
	hostname, err := validation.ValidateHostname(rawHostName)
	if err != nil || rawHostName == "" {
		return cli.ShowCommandHelp(c, "ssh")
	}
	originURL := ensureURLScheme(hostname)

	// get the headers from the cmdline and add them
	headers := buildRequestHeaders(c.StringSlice(sshHeaderFlag))
	if c.IsSet(sshTokenIDFlag) {
		headers.Set(h2mux.CFAccessClientIDHeader, c.String(sshTokenIDFlag))
	}
	if c.IsSet(sshTokenSecretFlag) {
		headers.Set(h2mux.CFAccessClientSecretHeader, c.String(sshTokenSecretFlag))
	}

	destination := c.String(sshDestinationFlag)
	if destination != "" {
		headers.Add(h2mux.CFJumpDestinationHeader, destination)
	}

	options := &carrier.StartOptions{
		OriginURL: originURL,
		Headers:   headers,
		Host:      hostname,
	}

	if connectTo := c.String(sshConnectTo); connectTo != "" {
		parts := strings.Split(connectTo, ":")
		switch len(parts) {
		case 1:
			options.OriginURL = fmt.Sprintf("https://%s", parts[0])
		case 2:
			options.OriginURL = fmt.Sprintf("https://%s:%s", parts[0], parts[1])
		case 3:
			options.OriginURL = fmt.Sprintf("https://%s:%s", parts[2], parts[1])
			options.TLSClientConfig = &tls.Config{
				InsecureSkipVerify: true,
				ServerName:         parts[0],
			}
			log.Warn().Msgf("Using insecure SSL connection because SNI overridden to %s", parts[0])
		default:
			return fmt.Errorf("invalid connection override: %s", connectTo)
		}
	}

	// we could add a cmd line variable for this bool if we want the SOCK5 server to be on the client side
	wsConn := carrier.NewWSConnection(log)

	if c.NArg() > 0 || c.IsSet(sshURLFlag) {
		forwarder, err := config.ValidateUrl(c, true)
		if err != nil {
			log.Err(err).Msg("Error validating origin URL")
			return errors.Wrap(err, "error validating origin URL")
		}
		log.Info().Str(LogFieldHost, forwarder.Host).Msg("Start Websocket listener")
		err = carrier.StartForwarder(wsConn, forwarder.Host, shutdownC, options)
		if err != nil {
			log.Err(err).Msg("Error on Websocket listener")
		}
		return err
	}

	return carrier.StartClient(wsConn, &carrier.StdinoutStream{}, options)
}

func buildRequestHeaders(values []string) http.Header {
	headers := make(http.Header)
	for _, valuePair := range values {
		split := strings.Split(valuePair, ":")
		if len(split) > 1 {
			headers.Add(strings.TrimSpace(split[0]), strings.TrimSpace(split[1]))
		}
	}
	return headers
}
