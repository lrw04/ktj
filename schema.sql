create table `submissions` (
    `id` integer primary key autoincrement,
    `user` text not null,
    `time` date not null,
    `language` text not null,
    `code` text not null,
    `problem` text not null,
    `verdict` text not null
);

create table `standings` (
    `user` text not null,
    `score` integer not null,
    `penalty` integer not null,
    `status` text not null
);
