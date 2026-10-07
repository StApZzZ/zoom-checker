// "Памятка для студентов" (TZ §33): text a teacher sends to the group.

/** TZ §33, verbatim. */
export const STUDENT_RESPONSIBILITY_TEXT =
  'Для автоматического учёта посещаемости необходимо указывать в Zoom имя и фамилию, соответствующие данным в списке учебной группы. Если имя участника не позволяет однозначно определить студента, система может не учитывать его присутствие. Ответственность за корректное отображаемое имя в Zoom лежит на участнике.'

export const NAMES_HEADING = 'Укажите в Zoom имя ровно так:'

export const RENAME_HEADING = 'Как указать имя в Zoom:'

// Zoom tells the service a new name only when the participant leaves, so a rename
// during the class is seen at once only after leaving and joining again.
export const RENAME_TIPS: readonly string[] = [
  'Лучше заранее: перед входом впишите фамилию и имя в поле «Ваше имя». Если входите с аккаунтом Zoom — нажмите на свой аватар → «Настройки» → «Профиль» и измените имя там.',
  'Если вы уже в конференции: на компьютере «Участники» → наведите курсор на своё имя → «Ещё» → «Переименовать»; на телефоне или планшете «Участники» → своё имя → «Переименовать».',
  'После переименования выйдите из конференции и зайдите снова по той же ссылке — иначе преподаватель увидит новое имя только когда вы выйдете. Если после входа вернулось старое имя, исправьте его в поле «Ваше имя» или в профиле Zoom.',
]

export function memoTitle(groupName: string): string {
  return `Памятка для студентов группы «${groupName}»`
}

/** Plain text of the memo for copying into a chat or an email. */
export function buildMemoText(groupName: string, names: readonly string[]): string {
  const parts = [memoTitle(groupName), STUDENT_RESPONSIBILITY_TEXT]
  if (names.length > 0) parts.push([NAMES_HEADING, ...names.map((n, i) => `${i + 1}. ${n}`)].join('\n'))
  parts.push([RENAME_HEADING, ...RENAME_TIPS.map((t) => `— ${t}`)].join('\n'))
  return parts.join('\n\n')
}
