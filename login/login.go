package login

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	qrcodeTerminal "github.com/Baozisoftware/qrcode-terminal-go"
	"github.com/tidwall/gjson"
)

func GetTvQrcodeUrlAndAuthCode() (string, string) {
	api := "https://passport.bilibili.com/x/passport-tv-login/qrcode/auth_code"
	data := make(map[string]string)
	data["local_id"] = "0"
	data["ts"] = fmt.Sprintf("%d", time.Now().Unix())
	signature(&data)
	dataString := strings.NewReader(mapToString(data))
	client := http.Client{}
	req, _ := http.NewRequest("POST", api, dataString)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("获取登录二维码失败:%s", err.Error())
		return "", ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	code := gjson.Parse(string(body)).Get("code").Int()
	if code == 0 {
		qrcodeUrl := gjson.Parse(string(body)).Get("data.url").String()
		authCode := gjson.Parse(string(body)).Get("data.auth_code").String()
		return qrcodeUrl, authCode
	} else {
		log.Printf("获取登录二维码失败:%d", code)
		return "", ""
	}
}

func VerifyLogin(authCode string, filename string) (err error) {
	api := "http://passport.bilibili.com/x/passport-tv-login/qrcode/poll"
	data := make(map[string]string)
	data["auth_code"] = authCode
	data["local_id"] = "0"
	data["ts"] = fmt.Sprintf("%d", time.Now().Unix())
	signature(&data)
	dataString := strings.NewReader(mapToString(data))
	client := http.Client{}
	req, _ := http.NewRequest("POST", api, dataString)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	select {
	case <-time.After(60 * time.Second):
		return nil
	default:
		for {
			resp, reqErr := client.Do(req)
			if reqErr != nil {
				return reqErr
			}
			body, _ := io.ReadAll(resp.Body)
			code := gjson.Parse(string(body)).Get("code").Int()
			if code == 0 {
				fmt.Println("登录成功")
				err = os.WriteFile(filename, body, 0644)
				if err != nil {
					return err
				}
				fmt.Println("cookie 已保存在", filename)
				break
			} else {
				time.Sleep(time.Second * 3)
			}
			err = resp.Body.Close()
		}
	}
	return err
}

var appkey = "4409e2ce8ffd12b8"
var appsec = "59b43e04ad6965f34319062b478f83dd"

func signature(params *map[string]string) {
	var keys []string
	(*params)["appkey"] = appkey
	for k := range *params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var query string
	for _, k := range keys {
		query += k + "=" + url.QueryEscape((*params)[k]) + "&"
	}
	query = query[:len(query)-1] + appsec
	hash := md5.New()
	hash.Write([]byte(query))
	(*params)["sign"] = hex.EncodeToString(hash.Sum(nil))
}

func mapToString(params map[string]string) string {
	var query string
	for k, v := range params {
		query += k + "=" + v + "&"
	}
	query = query[:len(query)-1]
	return query
}

func LoginBili() {
	fmt.Println("请最大化窗口，以确保二维码完整显示，回车继续")
	fmt.Scanf("%s", "")
	loginUrl, authCode := GetTvQrcodeUrlAndAuthCode()
	qrcode := qrcodeTerminal.New()
	qrcode.Get([]byte(loginUrl)).Print()
	fmt.Println("或将此链接复制到手机B站打开:", loginUrl)
	_ = VerifyLogin(authCode, "cookie.json")
}
