# libpassage.so exports Java_info_gluek_passage_Native_*: the class and its
# native methods must keep their names.
-keep class info.gluek.passage.Native {
    native <methods>;
}
