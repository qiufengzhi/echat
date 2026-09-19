import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'

import BackHeader from '../../components/layout/BackHeader'
import AvatarCrop from '../../components/avatar/AvatarCrop'
import { getAuthUser, updateAuthUser, type AuthUser } from '../../services/auth'
import { updateProfile, uploadAvatar } from '../../services/profile'

// 昵称长度上限（后端按 runes 校验，前端计数一致）
const NAME_MAX = 20

// EditProfilePage 我的-编辑资料子页：可上传头像、改昵称；用户名是唯一句柄只读展示
export default function EditProfilePage() {
  const navigate = useNavigate()
  const [user, setUser] = useState<AuthUser | null>(() => getAuthUser())
  const [name, setName] = useState(user?.displayName ?? '')
  const [avatarUrl, setAvatarUrl] = useState<string | null>(user?.avatarUrl ?? null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [crop, setCrop] = useState<Blob | null>(null)
  const [uploading, setUploading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [toast, setToast] = useState<string | null>(null)
  const fileRef = useRef<HTMLInputElement | null>(null)

  const nameRunes = [...name].length
  const trimmed = name.trim()
  const canSave = trimmed !== '' && trimmed !== user?.displayName && nameRunes <= NAME_MAX

  // 成功提示短暂展示后自动消失
  useEffect(() => {
    if (!toast) return
    const t = window.setTimeout(() => setToast(null), 2200)
    return () => window.clearTimeout(t)
  }, [toast])

  const handleBack = () => {
    if (window.history.length > 1) navigate(-1)
    else navigate('/')
  }

  // 头像来源弹层 → 拍照 / 相册 复用同一 file input，仅切换 capture 语义
  const pickSource = (capture: boolean) => {
    if (fileRef.current) fileRef.current.setAttribute('capture', capture ? 'user' : 'environment')
    setPickerOpen(false)
    fileRef.current?.click()
  }

  const onFile = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // 允许重复选择同一张图
    if (!file) return
    if (!/^image\/(jpeg|png|webp)$/i.test(file.type)) {
      setError('仅支持 JPG、PNG、WebP 图片')
      return
    }
    setError(null)
    setCrop(file)
  }

  const handleCropDone = async (blob: Blob) => {
    setCrop(null)
    setUploading(true)
    try {
      const next = await uploadAvatar(blob)
      updateAuthUser(next)
      setUser(next)
      setAvatarUrl(next.avatarUrl)
      setToast('头像已更新')
    } catch (err) {
      setError(err instanceof Error ? err.message : '头像上传失败，请稍后重试')
    } finally {
      setUploading(false)
    }
  }

  const handleSave = async () => {
    setSaving(true)
    setError(null)
    try {
      const next = await updateProfile({ displayName: trimmed })
      updateAuthUser(next)
      navigate('/', { replace: true }) // 成功即回我的 Tab，昵称/头像即时可见
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存失败，请稍后重试')
      setSaving(false)
    }
  }

  return (
    <div className="me-sub edit-profile-page">
      <BackHeader title="编辑资料" onBack={handleBack} />

      <div className="edit-ava">
        <button className="ring" type="button" onClick={() => setPickerOpen(true)} aria-label="更换头像">
          {avatarUrl ? <img src={avatarUrl} alt="" /> : <span className="init">{(user?.displayName || '我').slice(0, 1)}</span>}
          <span className="badge" aria-hidden="true">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
              <path d="M23 19a2 2 0 0 1-2 2H3a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4l2-3h6l2 3h4a2 2 0 0 1 2 2z" />
              <circle cx="12" cy="13" r="4" />
            </svg>
          </span>
        </button>
        <div className="hint">点击更换头像</div>
      </div>

      <div className="edit-card">
        <label className="edit-row">
          <span className="lb">昵称</span>
          <span className="bd">
            <input
              type="text"
              value={name}
              maxLength={NAME_MAX}
              placeholder="给自己起个好听的名字"
              onChange={e => setName(e.target.value)}
            />
            <span className="count">{nameRunes} / {NAME_MAX}</span>
          </span>
        </label>
        <div className="edit-row">
          <span className="lb">用户名</span>
          <span className="bd">
            <span className="ro">{user ? `@${user.username}` : ''}</span>
            <span className="sub">用户名是唯一标识，暂不支持修改</span>
          </span>
        </div>
      </div>

      {error && <p className="edit-error">{error}</p>}
      {toast && (
        <div className="edit-toast" role="status">
          {toast}
        </div>
      )}

      <div className="save-bar">
        <button type="button" disabled={!canSave || saving} onClick={() => void handleSave()}>
          {saving ? '保存中…' : '保存'}
        </button>
      </div>

      {/* 选图隐藏输入 */}
      <input ref={fileRef} type="file" accept="image/*" style={{ display: 'none' }} onChange={onFile} />

      {/* 头像来源底部弹层 */}
      {pickerOpen && (
        <>
          <div className="sheet-backdrop" onClick={() => setPickerOpen(false)} aria-hidden="true" />
          <section className="sheet ava-sheet" role="dialog" aria-label="更换头像">
            <div className="sheet-inner">
              <div className="handle" />
              <div className="ava-sheet-title">更换头像</div>
              <button className="ava-opt" type="button" onClick={() => pickSource(true)}>
                <span className="ic">📷</span>拍照
              </button>
              <button className="ava-opt" type="button" onClick={() => pickSource(false)}>
                <span className="ic">🖼️</span>从相册选择
              </button>
              <button
                className="ava-opt"
                type="button"
                disabled={!avatarUrl}
                onClick={() => window.open(avatarUrl ?? '', '_blank', 'noopener')}
              >
                <span className="ic">👀</span>查看头像
              </button>
              <button className="ava-opt cancel" type="button" onClick={() => setPickerOpen(false)}>
                取消
              </button>
            </div>
          </section>
        </>
      )}

      {/* 头像裁剪 */}
      {crop && !uploading && <AvatarCrop source={crop} onCancel={() => setCrop(null)} onDone={blob => void handleCropDone(blob)} />}
    </div>
  )
}