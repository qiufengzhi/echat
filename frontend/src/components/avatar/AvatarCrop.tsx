import { useEffect, useMemo, useRef, useState } from 'react'

// 裁剪固定参数：预览视口 272×272，输出 512×512 方形，最大放大 4 倍
const VIEW = 272
const OUT = 512
const ZOOM_MAX = 4

// AvatarCropProps 裁剪面板参数
interface AvatarCropProps {
  source: Blob // 用户选中的原图
  onCancel: () => void // 取消裁剪，返回编辑页
  onDone: (blob: Blob) => void // 完成：回调裁好的方形图
}

// AvatarCrop 1:1 方形头像裁剪：预览 canvas 与输出共用同一变换公式，
// 拖拽平移 + 滑块缩放，经 createImageBitmap 修正 iOS 拍照 EXIF 旋转
export default function AvatarCrop({ source, onCancel, onDone }: AvatarCropProps) {
  const [bmp, setBmp] = useState<ImageBitmap | null>(null)
  const [scale, setScale] = useState(1)
  const [off, setOff] = useState({ x: 0, y: 0 })
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const dragRef = useRef<{ px: number; py: number; ox: number; oy: number } | null>(null)

  // 用 createImageBitmap 解码并应用 EXIF 朝向，画布绘制以解码后尺寸为准
  useEffect(() => {
    let alive = true
    createImageBitmap(source, { imageOrientation: 'from-image' } as ImageBitmapOptions)
      .then(b => {
        if (alive) setBmp(b)
      })
      .catch(() => {
        if (alive) setBmp(null)
      })
    return () => {
      alive = false
    }
  }, [source])

  // cover 让图至少铺满视口的基准缩放
  const cover = useMemo(() => (bmp ? Math.max(VIEW / bmp.width, VIEW / bmp.height) : 1), [bmp])

  // clampOffsets 限制平移量，保证图片始终盖住视口不留空白
  const clampOffsets = (s: number, o: { x: number; y: number }) => {
    if (!bmp) return o
    const mx = Math.max(0, (bmp.width * s - VIEW) / 2)
    const my = Math.max(0, (bmp.height * s - VIEW) / 2)
    return { x: Math.min(mx, Math.max(-mx, o.x)), y: Math.min(my, Math.max(-my, o.y)) }
  }

  // sourceRect 由屏幕变换反推视口对应的源图裁剪区：屏幕坐标 = 视口中心 + (源x-宽/2)*s + 平移
  const sourceRect = (s: number, o: { x: number; y: number }) =>
    bmp
      ? {
          w: VIEW / s,
          h: VIEW / s,
          x: bmp.width / 2 - (VIEW / 2 + o.x) / s,
          y: bmp.height / 2 - (VIEW / 2 + o.y) / s,
        }
      : null

  const drawPreview = () => {
    const canvas = canvasRef.current
    if (!canvas || !bmp) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    const r = sourceRect(scale, off)
    if (!r) return
    ctx.clearRect(0, 0, VIEW, VIEW)
    ctx.drawImage(bmp, r.x, r.y, r.w, r.h, 0, 0, VIEW, VIEW)
  }

  // 每次状态变化（含 bmp 就绪）后重绘预览
  useEffect(() => drawPreview())

  // bmp 就绪后复位为「铺满」视角
  useEffect(() => {
    if (bmp) {
      setScale(cover)
      setOff({ x: 0, y: 0 })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bmp])

  const onPointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    e.currentTarget.setPointerCapture(e.pointerId)
    dragRef.current = { px: e.clientX, py: e.clientY, ox: off.x, oy: off.y }
  }
  const onPointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const d = dragRef.current
    if (!d) return
    setOff(
      clampOffsets(scale, {
        x: d.ox + (e.clientX - d.px),
        y: d.oy + (e.clientY - d.py),
      }),
    )
  }
  const onPointerUp = () => {
    dragRef.current = null
  }

  const setZoom = (factor: number) => {
    if (!bmp) return
    const next = Math.min(cover * ZOOM_MAX, Math.max(cover, scale * factor))
    setScale(next)
    setOff(clampOffsets(next, off))
  }

  const sliderValue = (() => {
    if (!bmp) return 0
    const span = cover * (ZOOM_MAX - 1)
    return span <= 0 ? 0 : Math.round(((scale - cover) / span) * 100)
  })()
  const onSlider = (value: number) => {
    if (!bmp) return
    const next = cover + (value / 100) * cover * (ZOOM_MAX - 1)
    setScale(next)
    setOff(clampOffsets(next, off))
  }

  const handleDone = () => {
    if (!bmp) return
    const r = sourceRect(scale, off)
    if (!r) return
    const canvas = document.createElement('canvas')
    canvas.width = OUT
    canvas.height = OUT
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    const keepAlpha = source.type === 'image/png'
    if (!keepAlpha) {
      ctx.fillStyle = '#fff'
      ctx.fillRect(0, 0, OUT, OUT)
    }
    ctx.drawImage(bmp, r.x, r.y, r.w, r.h, 0, 0, OUT, OUT)
    canvas.toBlob(
      blob => {
        if (blob) onDone(blob)
      },
      keepAlpha ? 'image/png' : 'image/jpeg',
      0.9,
    )
  }

  return (
    <div className="modal-layer" role="presentation">
      <section className="crop-card" role="dialog" aria-modal="true" aria-label="裁剪头像">
        <div className="crop-title">调整头像</div>
        {bmp ? (
          <>
            <div className="crop-frame">
              <canvas
                ref={canvasRef}
                width={VIEW}
                height={VIEW}
                className="crop-canvas"
                onPointerDown={onPointerDown}
                onPointerMove={onPointerMove}
                onPointerUp={onPointerUp}
                onPointerCancel={onPointerUp}
              />
              <span className="corner tl" />
              <span className="corner br" />
            </div>
            <div className="crop-tools">
              <button type="button" className="z" aria-label="缩小" onClick={() => setZoom(1 / 1.25)}>
                −
              </button>
              <input
                type="range"
                min={0}
                max={100}
                value={sliderValue}
                aria-label="缩放"
                onChange={e => onSlider(Number(e.target.value))}
              />
              <button type="button" className="z" aria-label="放大" onClick={() => setZoom(1.25)}>
                +
              </button>
            </div>
          </>
        ) : (
          <div className="crop-loading">正在处理图片…</div>
        )}
        <p className="crop-tip">拖动图片调整位置，滑块缩放大小</p>
        <div className="crop-actions">
          <button type="button" className="no" onClick={onCancel}>
            取消
          </button>
          <button type="button" className="ok" disabled={!bmp} onClick={handleDone}>
            完成
          </button>
        </div>
      </section>
    </div>
  )
}