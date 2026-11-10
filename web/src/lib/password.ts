/**
 * 密码规则的前端镜像。
 *
 * 与后端 internal/service/auth_service.go 的 ValidatePasswordStrength 一一对应：
 *   - 至少 10 位
 *   - 不超过 128 位
 *   - 不得与用户名相同（只折叠 ASCII 大小写）
 *   - 必须混合字母与数字或符号
 *
 * 这份清单存在的理由：前端要能在提交前告诉用户「哪里不达标」，
 * 而不是让用户填完提交后被后端拒绝。
 *
 * 规则本身以服务端为准；前端镜像只是为了体验，
 * 即便前端判定通过，服务端仍会再校验一次——这里不会成为安全边界。
 * 但两边的口径必须一致：口径一旦分叉，就会出现「界面全绿、提交被拒」，
 * 用户既看不到哪里不对，也无从修正。
 *
 * 因此下面的判定刻意与后端逐条对应：
 *   - 长度按字符（code point）计，不是字节，与后端的 utf8.RuneCountInString 对齐；
 *   - 「字母」用 Unicode 字母类别 \p{L}，与后端的 unicode.IsLetter 对齐
 *     （汉字、假名、谚文算字母；数字、标点、emoji 算「其他」）；
 *   - 用户名比较只折叠 ASCII 大小写，与后端的 asciiEqualFold 对齐。
 */

/** 单条规则的结果。 */
export interface PasswordRule {
  /** 规则的机器可读标识。 */
  id: 'length' | 'maxLength' | 'mix' | 'notUsername'
  /** 面向用户的中文描述。 */
  label: string
  /** 当前输入是否满足该规则。 */
  ok: boolean
}

/** Unicode 字母类别：汉字、假名、谚文、拉丁字母都算字母。 */
const LETTER_RE = /\p{L}/u

/** 非字母字符：数字、标点、空白、emoji。 */
const NON_LETTER_RE = /[^\p{L}]/u

/** 只折叠 ASCII 大小写，与 domain.CanonicalUsername 的口径一致。 */
function asciiFold(s: string): string {
  let out = ''
  for (const ch of s) {
    const code = ch.codePointAt(0) ?? 0
    out += code >= 0x41 && code <= 0x5a ? String.fromCharCode(code + 32) : ch
  }
  return out
}

/**
 * 校验密码，返回逐条规则的结果。
 *
 * username 为可选：新建账号时用户名可能还没填或正在改，
 * 此时「不得与用户名相同」这条按不满足处理（灰态），
 * 因为此时还无法判定。
 */
export function checkPasswordRules(password: string, username?: string): PasswordRule[] {
  // 按 code point 计长度，与后端按 rune 计数一致：
  // 用 length 会把代理对（emoji 等）算成两个字符，
  // 于是「一个 emoji」在前端算 2 位、后端算 1 位。
  const length = [...password].length

  const rules: PasswordRule[] = [
    {
      id: 'length',
      label: '至少 10 位',
      // 空密码时不亮红：用户还没开始输入就标红是噪音。
      ok: length === 0 || length >= 10,
    },
    {
      id: 'maxLength',
      label: '不超过 128 位',
      ok: length <= 128,
    },
    {
      id: 'mix',
      label: '混合字母与数字或符号',
      ok: length === 0 || (LETTER_RE.test(password) && NON_LETTER_RE.test(password)),
    },
  ]

  if (username !== undefined && username.length > 0) {
    rules.push({
      id: 'notUsername',
      label: '不得与用户名相同',
      ok: length === 0 || asciiFold(password) !== asciiFold(username),
    })
  }

  return rules
}

/**
 * 是否满足全部规则。
 *
 * 空密码返回 false：空值不是「还没输错」，而是「还没填」，
 * 调用方应据此决定是否禁用提交按钮。
 */
export function passwordSatisfiesAll(password: string, username?: string): boolean {
  if (password.length === 0) return false
  return checkPasswordRules(password, username).every((r) => r.ok)
}
