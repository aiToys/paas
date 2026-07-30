import type {
  AxiosInstance,
  AxiosError,
  InternalAxiosRequestConfig
} from 'axios'
import { parseProblem } from './problem'
import { notifyProblem } from './notify'
import { getAccessToken } from './token'
import { HttpError } from '@/lib/error/types'
import type { ApiResult } from './types'

// 扩展 config：silent 抑制全局错误提示
declare module 'axios' {
  interface AxiosRequestConfig {
    _silent?: boolean
  }
}

export interface AppAxiosRequestConfig extends InternalAxiosRequestConfig {
  _silent?: boolean
}

export function installInterceptors(instance: AxiosInstance): void {
  // 请求拦截：注入 Bearer Token
  instance.interceptors.request.use((config) => {
    const token = getAccessToken()
    if (token) {
      config.headers.set('Authorization', `Bearer ${token}`)
    }
    return config
  })

  // 响应拦截：解包 ApiResult + 解析 ProblemDetail
  instance.interceptors.response.use(
    (response) => {
      // 兼容两种成功响应包裹：
      //   - ApiResult：{ code, data, msg }（code===0 解包 data；!==0 当应用层错误）
      //   - PaaS core：{ data: T }（无 code，直接解包 data）
      const payload = response.data
      if (payload && typeof payload === 'object' && 'data' in payload) {
        const obj = payload as Record<string, unknown>
        if ('code' in obj) {
          // ApiResult 路径
          const code = obj.code as number
          if (code !== 0) {
            const status = code >= 400 && code < 600 ? code : 500
            const msg = (obj.msg as string) || 'Unknown error'
            const problem = parseProblem(status, {
              type: 'about:blank',
              title: msg,
              status,
              detail: msg
            })
            notifyProblem(problem, { silent: response.config._silent })
            throw new HttpError(problem, response as unknown as Response)
          }
        }
        // code===0 或 core {data:T}：统一解包 data
        response.data = obj.data
      }
      return response
    },
    (error: AxiosError) => {
      const status = error.response?.status ?? 0
      const body = error.response?.data
      const problem = parseProblem(status, body)
      notifyProblem(problem, {
        silent: (error.config as AppAxiosRequestConfig)?._silent
      })
      return Promise.reject(
        new HttpError(problem, error.response as unknown as Response)
      )
    }
  )
}
