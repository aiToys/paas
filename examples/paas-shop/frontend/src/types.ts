// 商品实体（bff /api/products 返回结构）
export interface Product {
  id: number
  name: string
  price: number
  category: string
  stock: number
  description: string
  imageUrl?: string
  createdAt?: string
}

// 订单行项与订单（bff /api/orders 返回结构；金额一律分（cents））
export interface OrderItem {
  productId: number
  name: string
  qty: number
  priceCents: number
}
export interface Order {
  id: string
  userId: string
  status: 'pending' | 'paid' | 'shipped' | 'completed' | 'refunded' | 'closed'
  totalCents: number
  items: OrderItem[]
  createdAt: string
  updatedAt: string
}
export interface ShopStats {
  orders: number
  gmvCents: number
  byStatus: Record<string, number>
}
