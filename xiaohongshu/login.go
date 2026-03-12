package xiaohongshu

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/sirupsen/logrus"
)

type LoginAction struct {
	page *rod.Page
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

// 已登录用户的标识元素
const loggedInSelector = ".main-container .user .link-wrapper .channel"

// 二维码相关选择器（扩展列表）
var qrcodeSelectors = []string{
	".qrcode-img",
	".qrcode-box img",
	"img[class*='qrcode']",
	".login-qrcode img",
	".qr-code img",
	"canvas[class*='qrcode']",
	"img[src*='qrcode']",
}

// 登录触发按钮选择器
var loginTriggerSelectors = []string{
	".login-btn",
	"[class*='login-btn']",
	".side-bar-component [class*='login']",
}

// cookie 同意弹窗选择器
var cookieConsentSelectors = []string{
	"button[class*='cookie']",
	"[class*='cookie-consent'] button",
	"[class*='cookie-banner'] button",
	".cookie-accept",
	"button:has-text('Accept')",
	"button:has-text('同意')",
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	pp := a.page.Context(ctx)

	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return false, fmt.Errorf("navigate failed: %w", err)
	}
	if err := pp.WaitLoad(); err != nil {
		return false, fmt.Errorf("wait load failed: %w", err)
	}

	time.Sleep(2 * time.Second)

	exists, _, err := pp.Has(loggedInSelector)
	if err != nil {
		return false, fmt.Errorf("check login status failed: %w", err)
	}

	return exists, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)

	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return fmt.Errorf("navigate failed: %w", err)
	}
	if err := pp.WaitLoad(); err != nil {
		return fmt.Errorf("wait load failed: %w", err)
	}

	time.Sleep(2 * time.Second)

	// 检查是否已经登录
	if exists, _, _ := pp.Has(loggedInSelector); exists {
		return nil
	}

	// 尝试触发登录弹窗
	a.triggerLoginModal(pp)

	// 等待扫码登录完成（使用轮询而不是 Element 阻塞）
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if exists, _, _ := pp.Has(loggedInSelector); exists {
			return nil
		}
		time.Sleep(1 * time.Second)
	}

	return fmt.Errorf("login timeout")
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	pp := a.page.Context(ctx)

	logrus.Info("开始导航到小红书首页...")

	// 导航到小红书首页
	if err := pp.Navigate("https://www.xiaohongshu.com/explore"); err != nil {
		return "", false, fmt.Errorf("navigate failed: %w", err)
	}
	if err := pp.WaitLoad(); err != nil {
		return "", false, fmt.Errorf("wait load failed: %w", err)
	}

	// 等待页面渲染
	time.Sleep(3 * time.Second)

	// 关闭可能出现的 cookie 同意弹窗
	a.dismissCookieConsent(pp)

	// 检查是否已经登录
	if exists, _, _ := pp.Has(loggedInSelector); exists {
		logrus.Info("已登录")
		return "", true, nil
	}

	logrus.Info("未登录，尝试获取二维码...")

	// 第一轮：检查二维码是否自动出现
	if src, err := a.findQrcodeImage(pp, 3*time.Second); err == nil {
		logrus.Info("二维码自动出现")
		return src, false, nil
	}
	logrus.Info("二维码未自动出现，尝试触发登录...")

	// 第二轮：尝试提取登录链接并直接导航
	if loginURL := a.extractLoginURL(pp); loginURL != "" {
		logrus.Infof("找到登录链接: %s", loginURL)
		if err := pp.Navigate(loginURL); err == nil {
			if err := pp.WaitLoad(); err == nil {
				time.Sleep(2 * time.Second)
				a.dismissCookieConsent(pp)
				if src, err := a.findQrcodeImage(pp, 10*time.Second); err == nil {
					logrus.Info("导航到登录页后找到二维码")
					return src, false, nil
				}
			}
		}
		logrus.Warn("导航到登录链接后未找到二维码")
	}

	// 第三轮：点击登录按钮（会导致页面导航）
	logrus.Info("尝试点击登录按钮...")
	a.clickLoginButton(pp)

	// 等待导航完成
	time.Sleep(3 * time.Second)
	_ = pp.WaitLoad()
	time.Sleep(2 * time.Second)

	// 关闭可能出现的 cookie 同意弹窗
	a.dismissCookieConsent(pp)

	// 在导航后的页面查找二维码
	if src, err := a.findQrcodeImage(pp, 15*time.Second); err == nil {
		logrus.Info("点击登录按钮后找到二维码")
		return src, false, nil
	}

	// 第四轮：尝试截图回退方案 - 用 JS 查找所有 img/canvas 并截图
	logrus.Info("尝试截图回退方案...")
	if src, err := a.screenshotQrcode(pp); err == nil {
		logrus.Info("通过截图方式获取到二维码")
		return src, false, nil
	}

	// 最后记录当前页面信息用于调试
	a.logPageState(pp)

	return "", false, fmt.Errorf("无法获取二维码，请检查网络或尝试使用 -headless=false 模式手动登录")
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	pp := a.page.Context(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastURL := ""
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			// 检查登录标识元素（当前页面可能已经是 /explore）
			if exists, _, _ := pp.Has(loggedInSelector); exists {
				logrus.Info("WaitForLogin: 检测到已登录元素")
				return true
			}

			// 检查 URL 变化 — 登录成功后页面可能从登录页跳转到首页
			info, err := pp.Info()
			if err != nil || info == nil {
				continue
			}
			currentURL := info.URL

			// 只在 URL 发生变化时（从登录页跳转到首页）才导航检查
			if currentURL != lastURL {
				logrus.Infof("WaitForLogin: URL 变化 %s -> %s", lastURL, currentURL)
				lastURL = currentURL

				// 如果跳转到了首页，说明登录可能成功了
				if strings.Contains(currentURL, "/explore") ||
					currentURL == "https://www.xiaohongshu.com/" {
					time.Sleep(2 * time.Second)
					if exists, _, _ := pp.Has(loggedInSelector); exists {
						logrus.Info("WaitForLogin: 跳转后检测到已登录")
						return true
					}
				}
			}
		}
	}
}

// dismissCookieConsent 关闭 cookie 同意弹窗
func (a *LoginAction) dismissCookieConsent(pp *rod.Page) {
	// 通过 CSS 选择器查找
	for _, sel := range cookieConsentSelectors {
		if has, el, _ := pp.Has(sel); has && el != nil {
			logrus.Infof("找到 cookie 同意弹窗，点击关闭: %s", sel)
			_ = el.Click(proto.InputMouseButtonLeft, 1)
			time.Sleep(500 * time.Millisecond)
			return
		}
	}

	// 通过 JS 查找并关闭
	_, _ = pp.Eval(`() => {
		// 查找 cookie 相关弹窗的关闭/接受按钮
		const buttons = document.querySelectorAll('button, a, span, div');
		for (const btn of buttons) {
			const text = btn.textContent?.trim()?.toLowerCase() || '';
			if (text === 'accept' || text === 'accept all' || text === '同意' ||
				text === '接受' || text === 'i agree' || text === 'ok' ||
				text === 'accept cookies' || text === 'your cookie preferences') {
				btn.click();
				return 'dismissed: ' + text;
			}
		}
		return 'no cookie consent found';
	}`)
}

// extractLoginURL 从页面中提取登录页面 URL
func (a *LoginAction) extractLoginURL(pp *rod.Page) string {
	// 尝试从 <a> 标签提取 href
	result, err := pp.Eval(`() => {
		// 查找登录按钮的链接
		const loginSelectors = [
			'.login-btn',
			'[class*="login-btn"]',
			'a[href*="login"]',
			'.side-bar-component a[href*="login"]',
		];
		for (const sel of loginSelectors) {
			const el = document.querySelector(sel);
			if (el) {
				// 如果是 <a> 标签，返回 href
				if (el.tagName === 'A' && el.href) {
					return el.href;
				}
				// 查找父级或子级的 <a> 标签
				const parentA = el.closest('a');
				if (parentA && parentA.href) {
					return parentA.href;
				}
				const childA = el.querySelector('a');
				if (childA && childA.href) {
					return childA.href;
				}
			}
		}
		return '';
	}`)
	if err != nil || result == nil {
		return ""
	}
	url, ok := result.Value.Val().(string)
	if !ok || url == "" {
		return ""
	}
	return url
}

// clickLoginButton 点击登录按钮（可能会触发页面导航）
func (a *LoginAction) clickLoginButton(pp *rod.Page) {
	// 方式1：通过选择器点击
	for _, sel := range loginTriggerSelectors {
		if has, el, _ := pp.Has(sel); has {
			logrus.Infof("点击登录按钮: %s", sel)
			_ = el.Click(proto.InputMouseButtonLeft, 1)
			return
		}
	}

	// 方式2：通过 JS 点击包含 "登录" 文字的元素
	logrus.Info("通过文字匹配查找登录按钮...")
	_, _ = pp.Eval(`() => {
		const allElements = document.querySelectorAll('span, a, button, div');
		for (const el of allElements) {
			const text = el.textContent?.trim();
			if (text === '登录' || text === '登录/注册') {
				el.click();
				return 'clicked: ' + text;
			}
		}
		const userArea = document.querySelector('.side-bar-component .user');
		if (userArea) {
			userArea.click();
			return 'clicked: sidebar user';
		}
		return 'no login button found';
	}`)
}

// triggerLoginModal 尝试多种方式触发登录弹窗（兼容旧版本）
func (a *LoginAction) triggerLoginModal(pp *rod.Page) {
	a.clickLoginButton(pp)
	time.Sleep(2 * time.Second)
}

// findQrcodeImage 在页面上查找二维码图片并返回 base64 数据
func (a *LoginAction) findQrcodeImage(pp *rod.Page, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		// 通过 CSS 选择器查找
		for _, sel := range qrcodeSelectors {
			has, el, _ := pp.Has(sel)
			if has && el != nil {
				visible, _ := el.Visible()
				if visible {
					logrus.Infof("找到二维码元素: %s", sel)
					return a.extractImageSource(el)
				}
			}
		}

		// 通过 JS 查找可能的二维码元素（canvas 或有特定属性的 img）
		result, err := pp.Eval(`() => {
			// 查找 canvas 元素（有些QR码用 canvas 渲染）
			const canvases = document.querySelectorAll('canvas');
			for (const canvas of canvases) {
				const rect = canvas.getBoundingClientRect();
				// QR 码通常是正方形，且尺寸在 100-400px 之间
				if (rect.width > 80 && rect.width < 500 &&
					Math.abs(rect.width - rect.height) < 20 &&
					rect.width > 0 && canvas.offsetParent !== null) {
					try {
						return { type: 'canvas', data: canvas.toDataURL('image/png') };
					} catch(e) {}
				}
			}

			// 查找 img 元素
			const imgs = document.querySelectorAll('img');
			for (const img of imgs) {
				const src = img.src || '';
				const cls = img.className || '';
				const alt = img.alt || '';
				const rect = img.getBoundingClientRect();
				// 检查是否可能是 QR 码
				if ((src.includes('qr') || src.includes('QR') ||
					 cls.includes('qr') || cls.includes('QR') ||
					 alt.includes('qr') || alt.includes('QR') ||
					 alt.includes('二维码') || alt.includes('扫码') ||
					 src.startsWith('data:image')) &&
					rect.width > 80 && rect.width < 500 &&
					img.offsetParent !== null) {
					return { type: 'img', src: src };
				}
			}

			return null;
		}`)
		if err == nil && result != nil && result.Value.Val() != nil {
			m, ok := result.Value.Val().(map[string]interface{})
			if ok {
				if t, _ := m["type"].(string); t == "canvas" {
					if data, _ := m["data"].(string); data != "" {
						logrus.Info("通过 JS canvas 获取到二维码")
						return data, nil
					}
				}
				if t, _ := m["type"].(string); t == "img" {
					if src, _ := m["src"].(string); src != "" {
						logrus.Info("通过 JS img 获取到二维码")
						return src, nil
					}
				}
			}
		}

		time.Sleep(500 * time.Millisecond)
	}

	return "", fmt.Errorf("二维码未在 %v 内出现", timeout)
}

// extractImageSource 从元素提取图片数据（始终返回 data URI）
func (a *LoginAction) extractImageSource(el *rod.Element) (string, error) {
	// 检查是否是 canvas
	tag, _ := el.Eval(`() => this.tagName.toLowerCase()`)
	if tag != nil && tag.Value.Str() == "canvas" {
		result, err := el.Eval(`() => this.toDataURL('image/png')`)
		if err == nil && result != nil && result.Value.Str() != "" {
			return result.Value.Str(), nil
		}
	}

	// 尝试获取 src 属性
	src, err := el.Attribute("src")
	if err == nil && src != nil && len(*src) > 0 {
		// 如果已经是 data URI，直接返回
		if len(*src) > 5 && (*src)[:5] == "data:" {
			return *src, nil
		}
		// 如果是 URL，截图该元素并返回 base64
		logrus.Infof("QR码是URL图片，使用截图方式获取: %s", (*src)[:min(80, len(*src))])
	}

	// 截图元素并返回 base64 data URI
	data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		return "", fmt.Errorf("元素截图失败: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("元素截图数据为空")
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return "data:image/png;base64," + b64, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// screenshotQrcode 截图方式获取二维码
func (a *LoginAction) screenshotQrcode(pp *rod.Page) (string, error) {
	// 查找可能的二维码区域并截图
	result, err := pp.Eval(`() => {
		// 查找可能包含二维码的容器
		const containers = document.querySelectorAll(
			'[class*="qrcode"], [class*="qr-code"], [class*="login-qr"], ' +
			'[class*="scan"], [class*="扫码"], [class*="code-container"]'
		);
		for (const container of containers) {
			const rect = container.getBoundingClientRect();
			if (rect.width > 50 && rect.height > 50 && container.offsetParent !== null) {
				return {
					found: true,
					selector: container.className,
					x: rect.x, y: rect.y,
					width: rect.width, height: rect.height,
				};
			}
		}
		return { found: false };
	}`)
	if err != nil {
		return "", fmt.Errorf("查找二维码容器失败: %w", err)
	}

	m, ok := result.Value.Val().(map[string]interface{})
	if !ok || m["found"] != true {
		return "", fmt.Errorf("未找到二维码容器")
	}

	logrus.Infof("找到二维码容器: %v", m["selector"])

	// 截取该区域的截图
	sel := fmt.Sprintf(".%s", m["selector"])
	if has, el, _ := pp.Has(sel); has {
		data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
		if err == nil && len(data) > 0 {
			b64 := base64.StdEncoding.EncodeToString(data)
			return "data:image/png;base64," + b64, nil
		}
	}

	// 全页面截图作为最后手段
	data, err := pp.Screenshot(true, &proto.PageCaptureScreenshot{
		Format: proto.PageCaptureScreenshotFormatPng,
	})
	if err != nil {
		return "", fmt.Errorf("全页面截图失败: %w", err)
	}
	if len(data) > 0 {
		b64 := base64.StdEncoding.EncodeToString(data)
		logrus.Warn("使用全页面截图作为回退（用户需要从截图中扫码）")
		return "data:image/png;base64," + b64, nil
	}

	return "", fmt.Errorf("截图失败")
}

// logPageState 记录当前页面状态用于调试
func (a *LoginAction) logPageState(pp *rod.Page) {
	result, err := pp.Eval(`() => {
		return {
			title: document.title,
			url: window.location.href,
			hasApp: !!document.querySelector('div#app'),
			hasLoginBtn: !!document.querySelector('[class*="login"]'),
			hasQrcode: !!document.querySelector('[class*="qrcode"]'),
			imgCount: document.querySelectorAll('img').length,
			canvasCount: document.querySelectorAll('canvas').length,
		}
	}`)
	if err != nil {
		logrus.Warnf("获取页面状态失败: %v", err)
		return
	}
	logrus.Infof("页面状态: %v", result.Value)
}
