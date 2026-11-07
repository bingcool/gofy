# support/image

图像处理：解码、缩放、裁剪、旋转、灰度、图片/文字水印、多格式编码与命名预设，
移植自 swoolefy 的 PHP `Image` 组件（Intervention Image）。Go 版只使用标准库
和 `golang.org/x/image`：**纯 Go、无 cgo，不需要 GD/Imagick 扩展**。

> 包名是 `image`，与标准库同名；业务代码建议别名导入：
> `gofyimage "github.com/bingcool/gofy/support/image"`。

## 快速开始

```go
import (
    "context"

    gofyimage "github.com/bingcool/gofy/support/image"
)

manager, err := gofyimage.NewManager(gofyimage.Config{
    BasePath:  "/data/resource",
    MaxPixels: 40_000_000,
    Encode:    gofyimage.EncodeOptions{Format: "jpeg", Quality: 82},
    Presets: map[string]gofyimage.Preset{
        "thumb": {Mode: gofyimage.ModeCover, Width: 200, Height: 200},
        "list":  {Mode: gofyimage.ModeContain, Width: 750},
    },
})
if err != nil {
    return err
}

result, err := manager.Open("/data/upload/a.jpg").
    ApplyPreset("list") // 也可以直接 Resize/Thumbnail/Crop
    Encode(nil)          // 返回 Binary、MIME、Extension、Size
```

`Manager` 是配置入口，无状态；`Editor` 持有一次编辑的位图，**请求级使用，
禁止跨 goroutine 共享**。同一张原图派生多档尺寸时，对每个 preset 重新
`Open`，不要复用一个 Editor。

## 配置

```yaml
image:
  base_path: /data/resource        # 相对路径基准，也是默认的读取白名单
  max_pixels: 40000000             # 0=默认 4000 万；-1=不限制
  allowed_roots:                   # 可选：Logo/字体可读取的根目录
    - /data/resource
  encode:
    format: jpeg                   # jpeg | png | gif；留空按来源协商
    quality: 82                    # jpeg 质量 1~100
    max_bytes: 8388608             # 可选：编码后体积上限
  watermark:
    image:
      path: watermark/logo.png
      position: bottom-right
      offset_x: 16
      offset_y: 16
      opacity: 0.6
    text:
      content: swoolefy
      font: font/DejaVuSans.ttf
      size: 28
      color: "#ffffff"
      opacity: 0.5
      position: bottom-right
      angle: 0
  presets:
    thumb:  {mode: cover, width: 200, height: 200}
    list:   {mode: contain, width: 750}
    avatar: {mode: cover, width: 128, height: 128, encode: {format: gif}}
```

`Config.Validate()` 只校验取值（模式、尺寸、质量、透明度、格式名），不会读取
水印文件；Logo/字体在真正应用水印时校验存在性与白名单。

## API 速查

| 方法 | 说明 |
| --- | --- |
| `NewManager(Config)` | 校验配置，`max_pixels` 默认 4000 万 |
| `Manager.Open(path)` | 从文件解码；先 `DecodeConfig` 预检像素 |
| `Manager.OpenBytes(data)` | 从二进制解码 |
| `Manager.Create(w, h, bg)` | 空白画布；`bg` 为空时透明 |
| `Editor.Size()` | 当前宽高 |
| `Editor.Resize(w, h, mode)` | 缩放，见下表 |
| `Editor.Thumbnail(w, h)` | 只缩不放，等比放进框内 |
| `Editor.Crop(x, y, w, h)` | 裁剪，越界返回 `ErrInvalidImage` |
| `Editor.Rotate(degrees)` | 旋转并扩展画布；90 的倍数精确重排 |
| `Editor.Greyscale()` | 按亮度转灰度，保留透明度 |
| `Editor.ApplyPreset(name)` | 按配置名缩放并记住 preset 的 encode 默认值 |
| `Editor.WatermarkImage(ImageWatermark)` | 叠加 Logo |
| `Editor.WatermarkText(TextWatermark)` | 渲染 TTF/OTF 文字水印 |
| `Editor.Encode(*EncodeOptions)` | 返回 `ProcessResult` |
| `Editor.Base64(*EncodeOptions)` | 返回编码后的标准 base64（无 data URI 前缀） |

### 缩放模式

| mode | 行为 |
| --- | --- |
| `contain` | 等比缩进框内，**只缩不放、不补白**；单维 0 表示不限制该维 |
| `cover` | 等比铺满后居中裁剪；宽高都必须大于 0 |
| `scale` | 等比缩放（允许放大）；单维时按该维 |
| `stretch`（别名 `fill`） | 双维时强制拉伸；单维时退化为等比缩放 |

### 水印

- 锚点：`top-left`、`top`、`top-right`、`left`、`center`、`right`、
  `bottom-left`、`bottom`、`bottom-right`；留空默认 `bottom-right`。
- `offset_x`/`offset_y` 从锚点向内偏移；`opacity ∈ (0,1]`。
- 图片水印路径与文字字体路径都会拒绝 `..` 段，解析后必须位于
  `allowed_roots`（未配置时限制在 `base_path`）内，防止配置注入导致任意文件读取。
- 文字水印需要可读的 TTF/OTF；中文需部署含汉字的字体。`angle` 与 `Rotate`
  方向一致，90 为顺时针。
- 预设与 `watermark` 配置**不会自动执行**，必须显式调用对应方法。

### 编码优先级

「全局 `encode` → preset 的 `encode` → `Encode(opts)`」后者覆盖前者。

| 情况 | 默认格式 |
| --- | --- |
| 任一层显式配置了 `format` | 使用该格式（`jpg` 归一为 `jpeg`） |
| 未配置，来源是 png / webp / gif | `png`，避免透明通道被压平 |
| 未配置，其余来源（含 `Create`） | `jpeg` |

纯 Go 实现**只支持导出 jpeg / png / gif**；webp 可解码，avif、bmp、tiff 等
返回 `ErrUnsupportedFormat`。JPEG 没有透明通道：透明像素会变成黑色，需要
保留透明时请显式输出 png。重新编码会丢弃 EXIF/GPS 等元数据（相当于 strip）；
渐进式 JPEG 不支持。

## 约束与错误

- `max_pixels` 同时约束解码、`Create`、`Resize`/`Crop` 的目标尺寸与水印图层，
  防止大图撑爆进程内存；`0` 使用默认 4000 万，`-1` 不限制（不推荐生产）。
- 图像以 `*image.NRGBA` 在内存中处理；`Editor` 不缓存、不共享。
- 错误全部可用 `errors.Is` 判断：

```text
ErrInvalidImage      来源无法解码、裁剪越界、预设名不存在、水印文件不可读
ErrTooLarge          像素数或编码后体积超限
ErrInvalidConfig     模式/预设/质量/透明度/颜色等配置非法
ErrUnsupportedFormat 输入或输出格式不支持
```

## 与 PHP 版的差异

- 无 `driver` 概念：不再区分 gd/imagick，统一纯 Go 实现，也不需要扩展检测；
- 导出格式以 jpeg/png/gif 为主；webp 仅解码，avif 不支持；
- 不提供 `strip`/`progressive` 开关：Go 重编码天然丢弃元数据，渐进式不支持；
- `Rotate` 任意角度都可旋转并扩展画布，90 的倍数走无插值精确重排；
- 函数式 API 取代抽象类；`Manager` 不缓存已解码图像；
- PHP 的 `toBase64`/`encodePngBinary` 由 `Base64()` 与
  `Encode(&EncodeOptions{Format: "png"})` 覆盖。

## 测试

```bash
go test ./support/image/
```

覆盖配置校验、缩放模式与预设、裁剪/旋转/灰度、像素上限、路径穿越与白名单、
图片/文字水印（含透明度与旋转）、格式协商、质量/体积上限与 base64。文字水印
测试使用 `golang.org/x/image/font/gofont` 的 TTF，不需要系统字体。