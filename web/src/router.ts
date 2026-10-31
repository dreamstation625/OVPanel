import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  scrollBehavior: () => ({ top: 0 }),
  routes: [
    { path: '/', redirect: '/overview' },
    { path: '/overview', component: () => import('./views/OverviewView.vue'), meta: { title: '概览' } },
    { path: '/users', component: () => import('./views/UsersView.vue'), meta: { title: '用户与客户端' } },
    { path: '/users/:id', component: () => import('./views/UserDetailView.vue'), meta: { title: '用户详情' } },
    { path: '/server', component: () => import('./views/ServerView.vue'), meta: { title: '服务端' } },
    {
      path: '/profiles/:id',
      component: () => import('./views/ProfileDetailView.vue'),
      meta: { title: '配置详情' },
    },
    { path: '/system', component: () => import('./views/SystemView.vue'), meta: { title: '系统' } },
    { path: '/:pathMatch(.*)*', redirect: '/overview' },
  ],
})

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = title ? `${title} · OVPanel` : 'OVPanel'
})
