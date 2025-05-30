package upload

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"
	"github.com/panjf2000/ants/v2"
	"github.com/schollz/progressbar/v3"
	"github.com/tidwall/gjson"
)

type Up struct {
	cookiePath string
	videosPath string

	videoTitle string // 视频标题
	videoDesc  string // 视频简介
	upType     int64  // 1:原创 2:转载
	coverPath  string // 封面路径
	tid        int64  // 分区id
	tag        string // 标签 , 分割
	source     string // 来源

	cookie string
	csrf   string
	client *req.Client

	upVideo *UpVideo

	threadNum int
	partChan  chan Part
	chunks    int64

	// 添加回调字段
	callback UploadCallback
}

type UpVideo struct {
	videoSize     int64
	videoName     string
	coverUrl      string
	auth          string
	uploadBaseUrl string
	biliFileName  string
	uploadId      string
	chunkSize     int64
	bizId         int64
	metaUposUrl   string
}

var wg sync.WaitGroup

func NewUp(cookiePath string, threadNum int) (*Up, error) {
	var cookieinfo CookieInfo
	loginInfo, err := os.ReadFile(cookiePath)
	if err != nil || len(loginInfo) == 0 {
		log.Printf("cookie文件不存在,请先登录:%+v", err)
		return nil, errors.New("cookie文件不存在,请先登录")
	}
	_ = json.Unmarshal(loginInfo, &cookieinfo)
	var cookie string
	var csrf string
	for _, v := range cookieinfo.Data.CookieInfo.Cookies {
		cookie += v.Name + "=" + v.Value + ";"
		if v.Name == "bili_jct" {
			csrf = v.Value
		}
	}
	var client = req.C().SetCommonHeaders(map[string]string{
		"user-agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/105.0.0.0 Safari/537.36 Edg/105.0.1343.53",
		"cookie":     cookie,
		"Connection": "keep-alive",
	})
	client.EnableDebugLog()
	resp, _ := client.R().Get("https://api.bilibili.com/x/web-interface/nav")
	uname := gjson.ParseBytes(resp.Bytes()).Get("data.uname").String()
	if uname == "" {
		return nil, errors.New("cookie失效,请重新登录")
	}
	log.Printf("%s 登录成功", uname)
	return &Up{
		cookiePath: cookiePath,
		cookie:     cookie,
		csrf:       csrf,
		client:     client,
		upVideo:    &UpVideo{},
		threadNum:  threadNum,
	}, nil
}

func (u *Up) SetVideos(tid, upType int64, videoPath, coverPath, title, desc, tag, source string) *Up {
	u.videosPath = videoPath
	u.videoTitle = title
	u.videoDesc = desc
	u.upType = upType
	u.tid = tid
	u.tag = tag
	u.source = source
	u.upVideo.videoName = path.Base(videoPath)
	u.upVideo.videoSize = u.getVideoSize()
	u.upVideo.coverUrl = u.uploadCover(coverPath)
	return u
}

func (u *Up) getVideoSize() int64 {
	file, err := os.Open(u.videosPath)
	if err != nil {
		log.Printf("获取视频长度失败:%s", err.Error())
		return 0
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		log.Printf("获取视频长度失败:%s", err.Error())
	}
	return fileInfo.Size()
}

func (u *Up) uploadCover(path string) string {
	if path == "" {
		return ""
	}
	var bytes []byte
	var err error
	if !strings.HasPrefix(path, "http") {
		bytes, err = os.ReadFile(path)
		if err != nil {
			log.Println(err)
			return ""
		}
	} else {
		resp, err := http.Get(path)
		if err != nil {
			log.Println("Error fetching image:", err)
			return ""
		}
		defer resp.Body.Close()
		// 创建一个新文件并将响应写入其中
		file, err := os.Create("image.jpg")
		if err != nil {
			log.Println("Error creating file:", err)
			return ""
		}
		defer file.Close()
		// 将响应体写入文件
		bytes, err = io.ReadAll(resp.Body)
		if err != nil {
			log.Println("Error writing to file:", err)
			return ""
		}
	}
	var base64Encoding string
	mimeType := http.DetectContentType(bytes)
	switch mimeType {
	case "image/jpeg", "image/jpg":
		base64Encoding = "data:image/jpeg;base64,"
	case "image/png":
		base64Encoding = "data:image/png;base64,"
	case "image/gif":
		base64Encoding = "data:image/gif;base64,"
	default:
		log.Println("不支持的图片格式")
	}
	base64Encoding += base64.StdEncoding.EncodeToString(bytes)
	var coverinfo CoverInfo
	u.client.R().SetFormDataFromValues(url.Values{
		"cover": {base64Encoding},
		"csrf":  {u.csrf},
	}).SetResult(&coverinfo).Post("https://member.bilibili.com/x/vu/web/cover/up")
	return coverinfo.Data.Url
}

// 添加设置回调的方法
func (u *Up) SetCallback(callback UploadCallback) *Up {
	u.callback = callback
	return u
}

// 修改Up方法
func (u *Up) Up() (*UploadResult, error) {
	result := &UploadResult{
		VideoSize:  u.upVideo.videoSize,
		VideoTitle: u.videoTitle,
		UploadTime: time.Now().Unix(),
	}

	// 获取预上传信息
	preupinfo := u.getPreUpInfo(u.upVideo.videoName, u.upVideo.videoSize, uploadProfile)
	// 设置上传参数
	upURI := strings.ReplaceAll(preupinfo.UposUri, "upos://", "")
	u.upVideo.uploadBaseUrl =
		fmt.Sprintf("https:%s/%s", preupinfo.Endpoint, upURI)
	u.upVideo.biliFileName = strings.Split(strings.Split(upURI, "/")[1], ".")[0]
	u.upVideo.chunkSize = preupinfo.ChunkSize
	u.upVideo.auth = preupinfo.Auth
	u.upVideo.bizId = preupinfo.BizId
	// u.upVideo.metaUposUrl = u.getPreUpInfo(u.upVideo.videoName, u.upVideo.videoSize, metaProfile).UposUri
	// 上传
	err := u.upload()
	if err != nil {
		result.Success = false
		result.Error = err
		result.Message = "上传失败: " + err.Error()

		// 调用回调函数
		if u.callback != nil {
			u.callback(result)
		}

		return result, err
	}
	// 设置投稿信息
	var addreq = AddReqJson{
		Copyright:    u.upType,
		Cover:        u.upVideo.coverUrl,
		Title:        u.videoTitle,
		Tid:          u.tid,
		Tag:          u.tag,
		DescFormatId: 16,
		Desc:         u.videoDesc,
		Source:       u.source,
		Dynamic:      "",
		Interactive:  0,
		Videos: []Video{
			{
				Filename: u.upVideo.biliFileName,
				Title:    u.upVideo.videoName,
				Desc:     "",
				Cid:      preupinfo.BizId,
			},
		},
		ActReserveCreate: 0,
		NoDisturbance:    0,
		NoReprint:        1,
		Subtitle: Subtitle{
			Open: 0,
			Lan:  "",
		},
		Dolby:         0,
		LosslessMusic: 0,
		Csrf:          u.csrf,
	}
	_ = addreq
	resp, err := u.client.R().SetQueryParams(map[string]string{
		"csrf": u.csrf,
	}).SetBodyJsonMarshal(addreq).Post("https://member.bilibili.com/x/vu/web/add/v3")

	if err != nil {
		result.Success = false
		result.Error = err
		result.Message = "投稿失败: " + err.Error()

		// 调用回调函数
		if u.callback != nil {
			u.callback(result)
		}

		return result, err
	}

	// 解析响应
	var uploadResp UploadResult
	err = json.Unmarshal(resp.Bytes(), &uploadResp)
	if err != nil {
		result.Success = false
		result.Error = err
		result.Message = "解析响应失败: " + err.Error()
	} else {
		// 复制响应数据到结果中
		result.Code = uploadResp.Code
		result.Message = uploadResp.Message
		result.Ttl = uploadResp.Ttl
		result.Data = uploadResp.Data

		if uploadResp.Code == 0 {
			result.Success = true
		} else {
			result.Success = false
		}
	}

	// 调用回调函数
	if u.callback != nil {
		u.callback(result)
	}

	log.Println(resp.String())
	return result, err
}

const uploadProfile = "ugcupos/bup"
const metaProfile = "fxmeta/bup"

func (u *Up) upload() error {
	defer ants.Release()
	if u.upVideo.videoSize == 0 {
		return errors.New(fmt.Sprintf("视频长度为0:%+v", u))
	}
	uploadParamMap := map[string]string{
		"profile":  uploadProfile,
		"filesize": strconv.FormatInt(u.upVideo.videoSize, 10),
		"partsize": strconv.FormatInt(u.upVideo.chunkSize, 10),
		"biz_id":   strconv.FormatInt(u.upVideo.bizId, 10),
	}
	var upinfo UpInfo
	rsp, err := u.client.SetCommonHeader(
		"X-Upos-Auth", u.upVideo.auth).R().
		SetQueryParams(uploadParamMap).SetResult(&upinfo).Post(u.upVideo.uploadBaseUrl + "?uploads&output=json")
	if err != nil {
		log.Printf("上传失败 req:%+v rsp:%+v err:%s", uploadParamMap, rsp, err.Error())
		return err
	}
	u.upVideo.uploadId = upinfo.UploadId
	u.chunks = int64(math.Ceil(float64(u.upVideo.videoSize) / float64(u.upVideo.chunkSize)))
	var reqjson = new(ReqJson)
	file, _ := os.Open(u.videosPath)
	defer file.Close()
	chunk := 0
	start := 0
	end := 0
	bar := progressbar.NewOptions(int(u.upVideo.videoSize/1024/1024),
		progressbar.OptionSetWriter(os.Stdout),
		progressbar.OptionSetItsString("MB"),
		progressbar.OptionSetDescription("视频上传中..."),
		progressbar.OptionSetWidth(50),
		progressbar.OptionShowIts(),
	)
	u.partChan = make(chan Part, u.chunks)
	go func() {
		for p := range u.partChan {
			reqjson.Parts = append(reqjson.Parts, p)
		}
	}()
	p, _ := ants.NewPool(u.threadNum)
	defer p.Release()
	for {
		buf := make([]byte, u.upVideo.chunkSize)
		size, err := file.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		buf = buf[:size]
		if size > 0 {
			wg.Add(1)
			end += size
			_ = p.Submit(u.uploadPartWrapper(chunk, start, end, size, buf, bar))
			buf = nil
			start += size
			chunk++
		}
		if err == io.EOF {
			break
		}
	}
	wg.Wait()
	close(u.partChan)
	jsonString, _ := json.Marshal(&reqjson)
	uploadParamMap = map[string]string{
		"output":   "json",
		"profile":  uploadProfile,
		"name":     u.upVideo.videoName,
		"uploadId": u.upVideo.uploadId,
		"biz_id":   strconv.FormatInt(u.upVideo.bizId, 10),
	}
	rsp, err = u.client.R().SetHeaders(map[string]string{
		"Content-Type": "application/json",
		"Origin":       "https://member.bilibili.com",
		"Referer":      "https://member.bilibili.com/",
	}).SetQueryParams(uploadParamMap).SetBodyString(string(jsonString)).SetResult(&upinfo).
		SetRetryCount(5).AddRetryHook(func(resp *req.Response, err error) {
		log.Println("重试发送分片确认请求")
		return
	}).AddRetryCondition(func(resp *req.Response, err error) bool {
		return err != nil || resp.StatusCode != 200
	}).Post(u.upVideo.uploadBaseUrl)
	if err != nil {
		log.Printf("上传失败 req:%+v rsp:%+v err:%s", uploadParamMap, rsp, err.Error())
		return err
	}
	return nil
}

func (u *Up) uploadPart(chunk int, start, end, size int, buf []byte, bar *progressbar.ProgressBar) {
	defer wg.Done()
	resp, _ := u.client.R().SetHeaders(map[string]string{
		"Content-Type": "application/octet-stream",
	}).SetQueryParams(map[string]string{
		"partNumber": strconv.Itoa(chunk + 1),
		"uploadId":   u.upVideo.uploadId,
		"chunk":      strconv.Itoa(chunk),
		"chunks":     strconv.Itoa(int(u.chunks)),
		"size":       strconv.Itoa(size),
		"start":      strconv.Itoa(start),
		"end":        strconv.Itoa(end),
		"total":      strconv.FormatInt(u.upVideo.videoSize, 10),
	}).SetBodyBytes(buf).SetRetryCount(5).AddRetryHook(func(resp *req.Response, err error) {
		log.Println("重试发送分片", chunk)
		return
	}).
		AddRetryCondition(func(resp *req.Response, err error) bool {
			return err != nil || resp.StatusCode != 200
		}).Put(u.upVideo.uploadBaseUrl)
	bar.Add(len(buf) / 1024 / 1024)
	if resp.StatusCode != 200 {
		log.Println("分片", chunk, "上传失败", resp.StatusCode, "size", size)
	}
	u.partChan <- Part{
		PartNumber: int64(chunk + 1),
		ETag:       "etag",
	}
}

type taskFunc func()

func (u *Up) uploadPartWrapper(chunk int, start, end, size int, buf []byte, bar *progressbar.ProgressBar) taskFunc {
	return func() {
		u.uploadPart(chunk, start, end, size, buf, bar)
	}
}

func (u *Up) getPreUpInfo(title string, totalSize int64, profile string) *PreUpInfo {
	var preUpInfo PreUpInfo
	u.client.R().SetQueryParams(map[string]string{
		"profile": profile,
		"name":    title,
		"size":    strconv.FormatInt(totalSize, 10),
		"r":       "upos",
		"ssl":     "0",
		"version": "2.11.0",
		"build":   "2110000",
	}).SetResult(&preUpInfo).Get("https://member.bilibili.com/preupload") // 移除URL中的查询参数
	return &preUpInfo
}
