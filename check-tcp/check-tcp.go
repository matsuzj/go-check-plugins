package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/mackerelio/checkers"
)

type tcpOpts struct {
	exchange
	Service  string  `long:"service"`
	Hostname string  `short:"H" long:"hostname" description:"Host name or IP Address"`
	Timeout  float64 `short:"t" long:"timeout" default:"10" description:"Seconds before connection times out"`
	MaxBytes int     `short:"m" long:"maxbytes"`
	// All      bool   `short:"A" long:"all" description:"All expect strings need to occur in server response. Default is any"`
	Delay    float64 `short:"d" long:"delay" description:"Seconds to wait between sending string and polling for response"`
	Warning  float64 `short:"w" long:"warning" description:"Response time to result in warning status (seconds)"`
	Critical float64 `short:"c" long:"critical" description:"Response time to result in critical status (seconds)"`
	Escape   bool    `short:"E" long:"escape" description:"Can use \\n, \\r, \\t or \\ in send or quit string. Must come before send or quit option. By default, nothing added to send, \\r\\n added to end of quit"`
}

type exchange struct {
	Send   string `short:"s" long:"send" description:"String to send to the server"`
	Expect string `short:"e" long:"expect" description:"String to expect in server response"`
	Quit   string `short:"q" long:"quit" description:"String to send server to initiate a clean close of the connection"`
	Port   int    `short:"p" long:"port" description:"Port number"`
	SSL    bool   `short:"S" long:"ssl" description:"Use SSL for the connection."`
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		os.Exit(1)
	}
	ckr := opts.run()
	ckr.Name = "TCP"
	if opts.Service != "" {
		ckr.Name = opts.Service
	}
	ckr.Exit()
}

func parseArgs(args []string) (*tcpOpts, error) {
	opts := &tcpOpts{}
	_, err := flags.ParseArgs(opts, args)
	return opts, err
}

func (opts *tcpOpts) prepare() error {
	opts.Service = strings.ToUpper(opts.Service)
	defaultEx := defaultExchange(opts.Service)
	opts.merge(defaultEx)

	if opts.Escape {
		opts.Quit = escapedString(opts.Quit)
		opts.Send = escapedString(opts.Send)
	} else if opts.Quit != "" {
		opts.Quit += "\r\n"
	}
	return nil
}

func defaultExchange(svc string) exchange {
	switch svc {
	case "FTP":
		return exchange{
			Port:   21,
			Expect: "220",
			Quit:   "QUIT",
		}
	case "POP":
		return exchange{
			Port:   110,
			Expect: "+OK",
			Quit:   "QUIT",
		}
	case "SPOP":
		return exchange{
			Port:   995,
			Expect: "+OK",
			Quit:   "QUIT",
			SSL:    true,
		}
	case "IMAP":
		return exchange{
			Port:   143,
			Expect: "* OK",
			Quit:   "a1 LOGOUT",
		}
	case "SIMAP":
		return exchange{
			Port:   993,
			Expect: "* OK",
			Quit:   "a1 LOGOUT",
			SSL:    true,
		}
	case "SMTP":
		return exchange{
			Port:   25,
			Expect: "220",
			Quit:   "QUIT",
		}
	case "SSMTP":
		return exchange{
			Port:   465,
			Expect: "220",
			Quit:   "QUIT",
			SSL:    true,
		}

	}

	return exchange{}
}

func (opts *tcpOpts) merge(ex exchange) {
	if opts.Port == 0 {
		opts.Port = ex.Port
	}
	if opts.Send == "" {
		opts.Send = ex.Send
	}
	if opts.Expect == "" {
		opts.Expect = ex.Expect
	}
	if opts.Quit == "" {
		opts.Quit = ex.Quit
	}
}

func dial(address string, ssl bool) (net.Conn, error) {
	if ssl {
		return tls.Dial("tcp", address, &tls.Config{})
	}
	return net.Dial("tcp", address)
}

func (opts *tcpOpts) run() *checkers.Checker {
	err := opts.prepare()
	if err != nil {
		return checkers.Unknown(err.Error())
	}

	send := opts.Send
	expect := opts.Expect
	quit := opts.Quit
	address := fmt.Sprintf("%s:%d", opts.Hostname, opts.Port)

	start := time.Now()
	if opts.Delay > 0 {
		time.Sleep(time.Duration(opts.Delay) * time.Second)
	}
	conn, err := dial(address, opts.SSL)
	if err != nil {
		return checkers.Critical(err.Error())
	}
	defer conn.Close()

	if send != "" {
		err := write(conn, []byte(send), opts.Timeout)
		if err != nil {
			return checkers.Critical(err.Error())
		}
	}

	res := ""
	if opts.Expect != "" {
		buf, err := slurp(conn, opts.MaxBytes, opts.Timeout)
		if err != nil {
			return checkers.Critical(err.Error())
		}

		res = string(buf)
		if expect != "" && !strings.HasPrefix(res, expect) {
			return checkers.Critical("Unexpected response from host/socket: " + res)
		}
	}

	if quit != "" {
		err := write(conn, []byte(quit), opts.Timeout)
		if err != nil {
			return checkers.Critical(err.Error())
		}
	}
	elapsed := time.Now().Sub(start)

	chkSt := checkers.OK
	if opts.Warning > 0 && elapsed > time.Duration(opts.Warning)*time.Second {
		chkSt = checkers.WARNING
	}
	if opts.Critical > 0 && elapsed > time.Duration(opts.Critical)*time.Second {
		chkSt = checkers.CRITICAL
	}

	return checkers.NewChecker(chkSt, fmt.Sprintf("%.3f seconds response time on %s port %d [%s]",
		float64(elapsed/time.Second), opts.Hostname, opts.Port, strings.Trim(res, "\r\n")))
}

func write(conn net.Conn, content []byte, timeout float64) error {
	if timeout > 0 {
		conn.SetWriteDeadline(time.Now().Add(time.Duration(timeout) * time.Second))
	}
	_, err := conn.Write(content)
	return err
}

func slurp(conn net.Conn, maxbytes int, timeout float64) ([]byte, error) {
	buf := []byte{}
	readLimit := 32 * 1024
	if maxbytes > 0 {
		readLimit = maxbytes
	}
	readBytes := 0
	if timeout > 0 {
		conn.SetReadDeadline(time.Now().Add(time.Duration(timeout) * time.Second))
	}
	for {
		tmpBuf := make([]byte, readLimit)
		i, err := conn.Read(tmpBuf)
		if err != nil {
			return buf, err
		}
		buf = append(buf, tmpBuf[:i]...)
		readBytes += i
		if i < readLimit || (maxbytes > 0 && maxbytes <= readBytes) {
			break
		}
	}
	return buf, nil
}

func escapedString(str string) (escaped string) {
	l := len(str)
	for i := 0; i < l; i++ {
		c := str[i]
		if c == '\\' && i+1 < l {
			i++
			c := str[i]
			switch c {
			case 'n':
				escaped += "\n"
			case 'r':
				escaped += "\r"
			case 't':
				escaped += "\t"
			case '\\':
				escaped += `\`
			default:
				escaped += `\` + string(c)
			}
		} else {
			escaped += string(c)
		}
	}
	return escaped
}
