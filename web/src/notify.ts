import { useMessage } from 'naive-ui'

export function useNotify() {
  const message = useMessage()
  return {
    success: (content: string) => message.success(content, { duration: 3200 }),
    error: (content: string) => message.error(content, { duration: 5000 }),
    warning: (content: string) => message.warning(content, { duration: 4200 }),
    info: (content: string) => message.info(content, { duration: 3200 }),
  }
}
