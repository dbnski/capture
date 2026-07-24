package main

import (
    "fmt"
    "strings"

    "gopkg.in/ini.v1"
    "github.com/go-sql-driver/mysql"
)

func loadConfigFile(c *mysql.Config, defaultsFile string, defaultsGroup string) (*mysql.Config, error) {
    if c == nil {
        c = mysql.NewConfig()
    }
    options := ini.LoadOptions{
        AllowBooleanKeys: true,
        IgnoreContinuation: true,
        IgnoreInlineComment: true,
    }

    params, err := ini.LoadSources(options, defaultsFile)
    if err != nil {
        return nil, err
    }

    for _, s := range params.Sections() {
        if s.Name() != defaultsGroup {
            continue
        }

        if c.User == "" && s.Key("user").String() != "" {
            c.User = s.Key("user").String()
        }
        if c.Passwd == "" && s.Key("password").String() != "" {
            c.Passwd = s.Key("password").String()
        }
        if c.Addr == "" && s.Key("socket").String() != "" {
            c.Net = "unix"
            c.Addr = s.Key("socket").String()
        }
        if c.Addr == "" && s.Key("host").String() != "" {
            c.Net = "tcp"
            c.Addr = s.Key("host").String()
        }
        if c.Addr == "" && s.Key("port").String() != "" {
            addr := strings.Split(c.Addr, ":")
            c.Addr = addr[0] + ":" + s.Key("port").String()
        }

        return c, nil
    }

    return nil, fmt.Errorf("section %q not found in %s", defaultsGroup, defaultsFile)
}
